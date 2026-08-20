// Package skills fetches, validates, and installs control-plane-managed
// OpenCode skills into the platform-owned global skills directory.
//
// It is a Go port of the upstream
// packages/sandbox-runtime/src/sandbox_runtime/managed_skills.py. The control
// plane serves a session-bound installation document (the "installation DTO")
// from GET /sessions/{id}/sandbox-skills; the sandbox re-validates every byte of
// it locally — paths, sizes, hashes, names, and modes — before any content
// reaches an OpenCode skill discovery path, then swaps the whole tree into place
// atomically.
//
// Materialization is boot work: it happens once, before OpenCode starts, so
// OpenCode restarts reuse the installed tree and never depend on control-plane
// availability.
package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Limits on an installation document. Mirrors the MAX_* constants upstream.
const (
	MaxSkillNameLength           = 64
	MaxSkillFiles                = 100
	MaxSkillFileBytes            = 256 * 1024
	MaxSkillRevisionBytes        = 1024 * 1024
	MaxSkillPathBytes            = 240
	MaxSkillPathDepth            = 10
	MaxManagedSkillsPerSession   = 20
	MaxManagedSkillManifestBytes = 5 * 1024 * 1024
	MaxManagedSkillResponseBytes = 32 * 1024 * 1024
)

var (
	// skillNameRE bounds a skill name to a lowercase kebab-case token, which is
	// also what makes it safe as a single directory name.
	skillNameRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	sha256RE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// yamlNameRE matches a `name:` line of SKILL.md YAML frontmatter, quoted or
	// bare. Group 1/2/3 hold the double-quoted, single-quoted, and bare value.
	yamlNameRE = regexp.MustCompile(`^\s*(?:name|"name"|'name')\s*:\s*(?:"([^"]+)"|'([^']+)'|([^#\s]+))`)
	// yamlNameFullRE is yamlNameRE anchored at both ends, matching Python's
	// fullmatch when reading the frontmatter of a to-be-installed skill.
	yamlNameFullRE = regexp.MustCompile(yamlNameRE.String() + `$`)
)

// Error is a managed-skill startup failure carrying a stable error code
// (fetch_failed, installation_too_large, installation_invalid, path_invalid,
// hash_mismatch, name_collision, install_failed).
type Error struct {
	Code string
	Msg  string
	Err  error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

// ErrorCode returns the stable code of a managed-skills failure, or "" for any
// other error. Callers log it so a boot failure is classifiable.
func ErrorCode(err error) string {
	if managed, ok := errors.AsType[*Error](err); ok {
		return managed.Code
	}
	return ""
}

func newError(code, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

func wrapError(code string, err error, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...), Err: err}
}

// File is one validated file of a managed skill. Content is the exact UTF-8
// text whose SHA-256 is SHA256 and whose byte length is SizeBytes.
type File struct {
	Path       string
	Content    string
	SHA256     string
	SizeBytes  int
	Executable bool
}

// Skill is one validated managed skill: a directory name plus its files, at
// least one of which is SKILL.md.
type Skill struct {
	Name  string
	Files []File
}

// Installation is a validated set of skills for one session. ManifestSHA256 is
// opaque here — the narrow DTO omits selection and assignment provenance, so it
// serves only as an identifier for logging.
type Installation struct {
	ManifestSHA256 string
	Skills         []Skill
}

// ValidateInstallation validates untrusted installation bytes independently of
// the control plane. Unknown fields are ignored so the contract can grow
// additively; everything the sandbox acts on is checked here.
func ValidateInstallation(raw []byte) (*Installation, error) {
	if len(raw) > MaxManagedSkillResponseBytes {
		return nil, newError("installation_too_large", "managed skills installation exceeds the size limit")
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// UseNumber keeps integers exact and distinguishable from floats, so the
	// integer checks below match Python's `isinstance(value, int)`.
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, wrapError("installation_invalid", err, "managed skills installation is not valid JSON")
	}
	installation, err := requireObject(document, []string{"schemaVersion", "manifestSha256", "skills"}, "installation")
	if err != nil {
		return nil, err
	}
	if version, ok := installation["schemaVersion"].(json.Number); !ok || version.String() != "1" {
		return nil, newError("installation_invalid", "unsupported managed skills schema version")
	}
	manifestSHA256, err := validateSHA256(installation["manifestSha256"], "manifest SHA-256")
	if err != nil {
		return nil, err
	}
	rawSkills, ok := installation["skills"].([]any)
	if !ok || len(rawSkills) > MaxManagedSkillsPerSession {
		return nil, newError("installation_invalid", "invalid managed skills list")
	}

	skills := make([]Skill, 0, len(rawSkills))
	names := make(map[string]bool, len(rawSkills))
	installationContentBytes := 0
	for _, rawSkill := range rawSkills {
		skill, err := validateSkill(rawSkill, names)
		if err != nil {
			return nil, err
		}
		for _, file := range skill.Files {
			installationContentBytes += file.SizeBytes
		}
		names[skill.Name] = true
		skills = append(skills, *skill)
	}

	if installationContentBytes > MaxManagedSkillManifestBytes {
		return nil, newError("installation_too_large", "managed skills content exceeds the session size limit")
	}
	return &Installation{ManifestSHA256: manifestSHA256, Skills: skills}, nil
}

// validateSkill validates one skill entry; names holds the skill names already
// accepted, so duplicates are rejected.
func validateSkill(rawSkill any, names map[string]bool) (*Skill, error) {
	skill, err := requireObject(rawSkill, []string{"name", "files"}, "skill")
	if err != nil {
		return nil, err
	}
	name, err := requireString(skill["name"], "skill name", false)
	if err != nil {
		return nil, err
	}
	if len(name) > MaxSkillNameLength || !skillNameRE.MatchString(name) {
		return nil, newError("installation_invalid", "invalid skill name: %q", name)
	}
	if names[name] {
		return nil, newError("installation_invalid", "duplicate managed skill name: %s", name)
	}
	rawFiles, ok := skill["files"].([]any)
	if !ok || len(rawFiles) == 0 || len(rawFiles) > MaxSkillFiles {
		return nil, newError("installation_invalid", "invalid skill files list")
	}

	files := make([]File, 0, len(rawFiles))
	paths := make(map[string]bool, len(rawFiles))
	revisionBytes := 0
	for _, rawFile := range rawFiles {
		file, err := validateFile(rawFile, paths)
		if err != nil {
			return nil, err
		}
		paths[file.Path] = true
		revisionBytes += file.SizeBytes
		files = append(files, *file)
	}
	if !paths["SKILL.md"] {
		return nil, newError("installation_invalid", "managed skill %s has no SKILL.md", name)
	}
	// The directory name and the name OpenCode reads out of the frontmatter must
	// agree, or the collision check below would guard the wrong identity.
	for _, file := range files {
		if file.Path != "SKILL.md" {
			continue
		}
		if frontmatterName(file.Content) != name {
			return nil, newError("installation_invalid", "SKILL.md name does not match managed skill %s", name)
		}
		break
	}
	if revisionBytes > MaxSkillRevisionBytes {
		return nil, newError("installation_invalid", "invalid total size for managed skill %s", name)
	}
	return &Skill{Name: name, Files: files}, nil
}

// validateFile validates one file entry; paths holds the file paths already
// accepted within the same skill, so duplicates and ancestor conflicts (a path
// that would have to be both a file and a directory) are rejected.
func validateFile(rawFile any, paths map[string]bool) (*File, error) {
	file, err := requireObject(rawFile, []string{"path", "content", "sha256", "sizeBytes", "executable"}, "skill file")
	if err != nil {
		return nil, err
	}
	path, err := validatePath(file["path"])
	if err != nil {
		return nil, err
	}
	if paths[path] {
		return nil, newError("installation_invalid", "duplicate skill file path: %s", path)
	}
	for existing := range paths {
		if strings.HasPrefix(path, existing+"/") || strings.HasPrefix(existing, path+"/") {
			return nil, newError("path_invalid", "conflicting skill file path: %s", path)
		}
	}
	content, err := requireString(file["content"], "skill file content", true)
	if err != nil {
		return nil, err
	}
	sizeBytes, err := requireInt(file["sizeBytes"], "skill file size")
	if err != nil {
		return nil, err
	}
	if len(content) > MaxSkillFileBytes || sizeBytes != len(content) {
		return nil, newError("installation_invalid", "invalid size for skill file %s", path)
	}
	digest, err := validateSHA256(file["sha256"], "skill file SHA-256")
	if err != nil {
		return nil, err
	}
	if sha256Hex([]byte(content)) != digest {
		return nil, newError("hash_mismatch", "SHA-256 mismatch for skill file %s", path)
	}
	executable, ok := file["executable"].(bool)
	if !ok {
		return nil, newError("installation_invalid", "invalid executable flag for %s", path)
	}
	// Only scripts/ may be executable, so a mode bit can never land on content
	// the agent merely reads.
	if executable && !strings.HasPrefix(path, "scripts/") {
		return nil, newError("path_invalid", "executable skill file must be under scripts/: %s", path)
	}
	return &File{Path: path, Content: content, SHA256: digest, SizeBytes: sizeBytes, Executable: executable}, nil
}

// requireObject returns value as a JSON object, erroring unless every key in
// keys is present. Extra keys are allowed (additive contract changes).
func requireObject(value any, keys []string, context string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, newError("installation_invalid", "invalid %s object", context)
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return nil, newError("installation_invalid", "invalid %s object", context)
		}
	}
	return object, nil
}

// requireString returns value as a non-empty (unless allowEmpty) UTF-8 string.
// Go's JSON decoder replaces malformed UTF-8 with U+FFFD rather than failing, so
// invalid encodings that survive decoding are rejected here — and would fail the
// SHA-256 check regardless.
func requireString(value any, context string, allowEmpty bool) (string, error) {
	text, ok := value.(string)
	if !ok || (text == "" && !allowEmpty) {
		return "", newError("installation_invalid", "invalid %s", context)
	}
	if !utf8.ValidString(text) {
		return "", newError("installation_invalid", "invalid UTF-8 in %s", context)
	}
	return text, nil
}

// requireInt returns value as a non-negative integer. JSON booleans, floats, and
// fractional numbers are rejected, as are values past the response cap (an
// overflow guard; a real size is cross-checked against the content length).
func requireInt(value any, context string) (int, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, newError("installation_invalid", "invalid %s", context)
	}
	parsed, err := number.Int64()
	if err != nil || parsed < 0 || parsed > MaxManagedSkillResponseBytes {
		return 0, newError("installation_invalid", "invalid %s", context)
	}
	return int(parsed), nil
}

func validateSHA256(value any, context string) (string, error) {
	digest, err := requireString(value, context, false)
	if err != nil {
		return "", err
	}
	if !sha256RE.MatchString(digest) {
		return "", newError("installation_invalid", "invalid %s", context)
	}
	return digest, nil
}

// validatePath accepts only a relative, forward-slash POSIX path with no
// traversal, control characters, backslashes, or empty segments — every path is
// joined onto a fresh staging directory, so this is the boundary that keeps
// writes inside it.
func validatePath(value any) (string, error) {
	path, err := requireString(value, "skill file path", false)
	if err != nil {
		return "", err
	}
	parts := strings.Split(path, "/")
	unsafe := strings.HasPrefix(path, "/") ||
		strings.Contains(path, `\`) ||
		len(path) > MaxSkillPathBytes ||
		len(parts) > MaxSkillPathDepth
	for _, character := range path {
		if character < 32 || character == 127 {
			unsafe = true
		}
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			unsafe = true
		}
	}
	if unsafe {
		return "", newError("path_invalid", "unsafe skill file path: %q", path)
	}
	return path, nil
}

// frontmatterName returns the `name` declared in the SKILL.md YAML frontmatter,
// or "" when the document has no frontmatter or no name before it closes. Only
// the first `---`-delimited block is considered, matching OpenCode's own read.
func frontmatterName(markdown string) string {
	if !strings.HasPrefix(markdown, "---\n") {
		return ""
	}
	for _, line := range splitLines(markdown)[1:] {
		if line == "---" {
			return ""
		}
		if match := yamlNameFullRE.FindStringSubmatch(line); match != nil {
			return firstNonEmpty(match[1:])
		}
	}
	return ""
}

// splitLines splits on \n and drops a trailing \r, the line endings that can
// appear in a skill document.
func splitLines(text string) []string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

func firstNonEmpty(values []string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func sha256Hex(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
