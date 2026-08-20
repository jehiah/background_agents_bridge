package sessiondiff

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jehiah/background_agents_bridge/internal/repomanifest"
)

// RepositoryListPath is the boot-disk copy of SESSION_CONFIG.repositories,
// written first-boot-only by the VM's startup script and the only repository
// state that survives a resume. The boot manifest is derived from it, so a
// baseline written back here is still there after a restore.
const RepositoryListPath = "/etc/oi/repositories.json"

// baselineField is the key both files carry the baseline under. The control
// plane may instead send the camelCase spelling, which repomanifest accepts on
// read; anything this bridge writes uses the snake_case form the rest of the
// repository list is written in.
const baselineField = "base_sha"

// ResolveBaselines makes sure every session repository has a diff baseline.
//
// The baseline is the commit the session started from, and it must not move for
// the life of the session — otherwise the viewer's "what did the agent change"
// answer shrinks every time the agent commits. The control plane supplies it in
// SESSION_CONFIG.repositories, which the startup script persists verbatim and
// passes through into the manifest. When it is absent (an older control plane,
// or a repository added by other means) this resolves the checkout's current
// HEAD once, at boot, before the agent can touch anything, and writes it back
// to both files: the manifest so this boot's captures can use it, and the
// persisted list so a resumed session keeps the original baseline instead of
// re-anchoring to the agent's own commits.
//
// It is best effort: a repository whose HEAD cannot be read is left without a
// baseline and is reported as unavailable in the bundle rather than blocking
// the boot.
func ResolveBaselines(ctx context.Context, manifestPath, listPath string, log *slog.Logger) {
	manifest, entries, err := loadManifestRecords(manifestPath)
	if err != nil {
		log.Warn("session_diff.baseline_manifest_unreadable", "path", manifestPath, "error", err.Error())
		return
	}
	if len(entries) == 0 {
		return
	}

	resolved := map[string]string{} // "owner/name" -> baseline
	for _, entry := range entries {
		if baselineOf(entry) != "" {
			continue
		}
		path, _ := entry["path"].(string)
		owner, _ := entry["repo_owner"].(string)
		name, _ := entry["repo_name"].(string)
		if path == "" {
			continue
		}
		baseline, err := resolveHead(ctx, path)
		if err != nil {
			log.Warn("session_diff.baseline_unresolved",
				"repository", owner+"/"+name, "path", path, "error", err.Error())
			continue
		}
		entry[baselineField] = baseline
		resolved[strings.ToLower(owner+"/"+name)] = baseline
		log.Info("session_diff.baseline_resolved",
			"repository", owner+"/"+name, "base_sha", baseline, "source", "checkout_head")
	}
	if len(resolved) == 0 {
		return
	}

	if err := writeJSONFile(manifestPath, manifest); err != nil {
		log.Warn("session_diff.baseline_manifest_write_failed", "path", manifestPath, "error", err.Error())
	}
	if err := applyBaselinesToList(listPath, resolved); err != nil {
		// The manifest still carries the baselines, so this boot is fine; only a
		// resume would re-anchor.
		log.Warn("session_diff.baseline_list_write_failed", "path", listPath, "error", err.Error())
	}
}

// loadManifestRecords reads the manifest as generic records so unknown keys
// survive the rewrite. It returns the whole document and its repositories
// array, sharing the same underlying maps.
func loadManifestRecords(path string) (map[string]any, []map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, nil, err
	}
	return document, records(document["repositories"]), nil
}

// applyBaselinesToList writes the resolved baselines into the persisted
// repository list, matching entries by identity and leaving every other key
// untouched.
func applyBaselinesToList(path string, resolved map[string]string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	changed := false
	for _, entry := range records(list) {
		if baselineOf(entry) != "" {
			continue
		}
		owner, _ := entry["repo_owner"].(string)
		name, _ := entry["repo_name"].(string)
		if baseline, ok := resolved[strings.ToLower(owner+"/"+name)]; ok {
			entry[baselineField] = baseline
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return writeJSONFile(path, list)
}

// records narrows a decoded JSON array to its object elements.
func records(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if record, ok := item.(map[string]any); ok {
			out = append(out, record)
		}
	}
	return out
}

// baselineOf returns the entry's valid baseline, if any.
func baselineOf(entry map[string]any) string {
	for _, key := range []string{baselineField, "baseSha"} {
		if value, ok := entry[key].(string); ok && repomanifest.IsObjectName(value) {
			return value
		}
	}
	return ""
}

// resolveHead reads a checkout's current commit.
func resolveHead(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "HEAD")
	cmd.Dir = dir
	cmd.Env = gitEnv()
	raw, err := cmd.Output()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if !repomanifest.IsObjectName(value) {
		return "", fmt.Errorf("unexpected HEAD value %q", truncate(value, 64))
	}
	return value, nil
}

// writeJSONFile rewrites path atomically, keeping its current permissions —
// the repository list is written with a restrictive umask and must stay that
// way.
func writeJSONFile(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')

	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
