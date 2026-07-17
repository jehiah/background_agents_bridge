// Package repomanifest reads the canonical session repository manifest.
//
// The supervisor derives the repository list once from SESSION_CONFIG and writes
// it to ManifestPath before any child process starts, rewriting it on every
// boot. Every other consumer — the bridge's push targeting and the
// create-pull-request tool — reads that manifest instead of re-deriving the
// /workspace/<repo_name> convention, so the checkout layout has exactly one
// authority.
//
// It is a Go port of load_repo_manifest / find_repo_entry in the upstream
// packages/sandbox-runtime/src/sandbox_runtime/repo_config.py. The manifest is
// JSON: {"repositories": [{owner, name, branch, path}]}.
package repomanifest

import (
	"encoding/json"
	"os"
	"strings"
)

// ManifestPath is the supervisor-written manifest location, mirrored from
// REPO_MANIFEST_FILE_PATH in the upstream constants.py.
const ManifestPath = "/tmp/oi-repo-manifest.json"

// Entry is one repository of the session workspace, in position order (first =
// primary). Matches RepoEntry in repo_config.py.
type Entry struct {
	Owner  string `json:"repo_owner"`
	Name   string `json:"repo_name"`
	Branch string `json:"branch"`
	Path   string `json:"path"`
}

// FullName is the "owner/name" identity used for matching and error messages.
func (e Entry) FullName() string {
	return e.Owner + "/" + e.Name
}

type manifestFile struct {
	Repositories []Entry `json:"repositories"`
}

// Load reads the supervisor-written manifest at path, returning an empty slice
// when the file is missing or malformed (mirroring load_repo_manifest). The
// repositories array is used verbatim from upstream — entries are returned in
// position order with no normalization.
func Load(path string) []Entry {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var mf manifestFile
	if err := json.Unmarshal(raw, &mf); err != nil {
		return nil
	}
	return mf.Repositories
}

// Find returns the entry whose identity matches owner/name case-insensitively.
// The returned entry carries the canonical casing and path — callers must use
// its fields, never the lookup arguments. Matches find_repo_entry.
func Find(entries []Entry, owner, name string) (Entry, bool) {
	ownerKey := strings.ToLower(owner)
	nameKey := strings.ToLower(name)
	for _, e := range entries {
		if strings.ToLower(e.Owner) == ownerKey && strings.ToLower(e.Name) == nameKey {
			return e, true
		}
	}
	return Entry{}, false
}
