package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// installationDocument builds a minimal one-skill, one-file installation DTO as
// the control plane would serve it. Tests mutate the returned map to exercise
// each rejection.
func installationDocument(name, path, content string) map[string]any {
	if content == "" {
		content = "---\nname: " + name + "\ndescription: \"Managed skill\"\n---\n# Managed\n"
	}
	digest := sha256.Sum256([]byte(content))
	return map[string]any{
		"schemaVersion":  1,
		"manifestSha256": strings.Repeat("a", 64),
		"skills": []any{
			map[string]any{
				"name": name,
				"files": []any{
					fileEntry(path, content, hex.EncodeToString(digest[:])),
				},
			},
		},
	}
}

func fileEntry(path, content, digest string) map[string]any {
	return map[string]any{
		"path":       path,
		"content":    content,
		"sha256":     digest,
		"sizeBytes":  len(content),
		"executable": false,
	}
}

func hashedFileEntry(path, content string) map[string]any {
	digest := sha256.Sum256([]byte(content))
	return fileEntry(path, content, hex.EncodeToString(digest[:]))
}

func encode(t *testing.T, document any) []byte {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	return raw
}

// skillFiles returns the mutable files list of the document's first skill.
func skillFiles(document map[string]any) []any {
	return document["skills"].([]any)[0].(map[string]any)["files"].([]any)
}

func setSkillFiles(document map[string]any, files []any) {
	document["skills"].([]any)[0].(map[string]any)["files"] = files
}

func TestValidateInstallationRejectsTraversalPaths(t *testing.T) {
	for _, path := range []string{"../escape", "scripts/../../escape", "/absolute", `a\b`, "a//b", "nested/./here"} {
		t.Run(path, func(t *testing.T) {
			_, err := ValidateInstallation(encode(t, installationDocument("managed", path, "")))
			if ErrorCode(err) != "path_invalid" {
				t.Fatalf("got %v (code %q), want path_invalid", err, ErrorCode(err))
			}
		})
	}
}

func TestValidateInstallationRejectsFileHashMismatch(t *testing.T) {
	document := installationDocument("managed", "SKILL.md", "")
	skillFiles(document)[0].(map[string]any)["sha256"] = strings.Repeat("0", 64)

	_, err := ValidateInstallation(encode(t, document))
	if ErrorCode(err) != "hash_mismatch" {
		t.Fatalf("got %v (code %q), want hash_mismatch", err, ErrorCode(err))
	}
}

func TestValidateInstallationRejectsMismatchedFrontmatterName(t *testing.T) {
	document := installationDocument("managed", "SKILL.md", "---\nname: other\n---\n")

	_, err := ValidateInstallation(encode(t, document))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("got %v, want a frontmatter name mismatch", err)
	}
}

func TestValidateInstallationRejectsFileAncestorConflictsInEitherOrder(t *testing.T) {
	for _, order := range [][2]string{
		{"references", "references/guide.md"},
		{"references/guide.md", "references"},
	} {
		t.Run(order[0], func(t *testing.T) {
			document := installationDocument("managed", "SKILL.md", "")
			files := skillFiles(document)
			for _, path := range order {
				files = append(files, hashedFileEntry(path, "content for "+path))
			}
			setSkillFiles(document, files)

			_, err := ValidateInstallation(encode(t, document))
			if err == nil || !strings.Contains(err.Error(), "conflicting skill file path") {
				t.Fatalf("got %v, want a conflicting path rejection", err)
			}
		})
	}
}

func TestValidateInstallationIgnoresAdditiveContractFields(t *testing.T) {
	document := installationDocument("managed", "SKILL.md", "")
	document["futureManifestField"] = true
	document["skills"].([]any)[0].(map[string]any)["futureSkillField"] = "value"
	skillFiles(document)[0].(map[string]any)["futureFileField"] = 1

	installation, err := ValidateInstallation(encode(t, document))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(installation.Skills) != 1 || installation.Skills[0].Name != "managed" {
		t.Fatalf("got %+v, want the managed skill", installation.Skills)
	}
}

func TestValidateInstallationRejectsMalformedDocuments(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		code   string
	}{
		{"missing skills key", func(d map[string]any) { delete(d, "skills") }, "installation_invalid"},
		{"unsupported schema version", func(d map[string]any) { d["schemaVersion"] = 2 }, "installation_invalid"},
		{"float schema version", func(d map[string]any) { d["schemaVersion"] = 1.5 }, "installation_invalid"},
		{"bad manifest digest", func(d map[string]any) { d["manifestSha256"] = "nope" }, "installation_invalid"},
		{"uppercase skill name", func(d map[string]any) {
			d["skills"].([]any)[0].(map[string]any)["name"] = "Managed"
		}, "installation_invalid"},
		{"empty files list", func(d map[string]any) { setSkillFiles(d, []any{}) }, "installation_invalid"},
		{"missing SKILL.md", func(d map[string]any) {
			setSkillFiles(d, []any{hashedFileEntry("other.md", "text")})
		}, "installation_invalid"},
		{"size disagrees with content", func(d map[string]any) {
			skillFiles(d)[0].(map[string]any)["sizeBytes"] = 3
		}, "installation_invalid"},
		{"boolean size", func(d map[string]any) {
			skillFiles(d)[0].(map[string]any)["sizeBytes"] = true
		}, "installation_invalid"},
		{"non-boolean executable", func(d map[string]any) {
			skillFiles(d)[0].(map[string]any)["executable"] = "yes"
		}, "installation_invalid"},
		{"duplicate skill name", func(d map[string]any) {
			skills := d["skills"].([]any)
			d["skills"] = append(skills, skills[0])
		}, "installation_invalid"},
		{"duplicate file path", func(d map[string]any) {
			files := skillFiles(d)
			setSkillFiles(d, append(files, files[0]))
		}, "installation_invalid"},
		{"executable outside scripts/", func(d map[string]any) {
			file := hashedFileEntry("run.sh", "#!/bin/sh\n")
			file["executable"] = true
			setSkillFiles(d, append(skillFiles(d), file))
		}, "path_invalid"},
		{"too deep a path", func(d map[string]any) {
			deep := strings.Repeat("a/", MaxSkillPathDepth) + "file.md"
			setSkillFiles(d, append(skillFiles(d), hashedFileEntry(deep, "text")))
		}, "path_invalid"},
		{"control character in path", func(d map[string]any) {
			setSkillFiles(d, append(skillFiles(d), hashedFileEntry("a\nb.md", "text")))
		}, "path_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := installationDocument("managed", "SKILL.md", "")
			test.mutate(document)

			_, err := ValidateInstallation(encode(t, document))
			if ErrorCode(err) != test.code {
				t.Fatalf("got %v (code %q), want %s", err, ErrorCode(err), test.code)
			}
		})
	}
}

func TestValidateInstallationRejectsNonJSON(t *testing.T) {
	_, err := ValidateInstallation([]byte("not json"))
	if ErrorCode(err) != "installation_invalid" {
		t.Fatalf("got %v (code %q), want installation_invalid", err, ErrorCode(err))
	}
}

func TestValidateInstallationAcceptsExecutableScript(t *testing.T) {
	document := installationDocument("managed", "SKILL.md", "")
	script := hashedFileEntry("scripts/run.sh", "#!/bin/sh\necho hi\n")
	script["executable"] = true
	setSkillFiles(document, append(skillFiles(document), script))

	installation, err := ValidateInstallation(encode(t, document))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(installation.Skills[0].Files) != 2 {
		t.Fatalf("got %d files, want 2", len(installation.Skills[0].Files))
	}
}

func TestFrontmatterName(t *testing.T) {
	tests := []struct {
		markdown string
		want     string
	}{
		{"---\nname: managed\n---\n", "managed"},
		{"---\nname: \"managed\"\n---\n", "managed"},
		{"---\nname: 'managed'\n---\n", "managed"},
		{"---\ndescription: x\nname: managed\n---\n", "managed"},
		{"---\n---\nname: managed\n", ""},
		{"# no frontmatter\nname: managed\n", ""},
		{"---\nname: managed extra\n---\n", ""},
	}
	for _, test := range tests {
		if got := frontmatterName(test.markdown); got != test.want {
			t.Errorf("frontmatterName(%q) = %q, want %q", test.markdown, got, test.want)
		}
	}
}
