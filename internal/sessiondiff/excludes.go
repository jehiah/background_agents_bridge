package sessiondiff

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// Markers delimiting the runtime-owned block in a checkout's info/exclude.
const (
	excludeBeginMarker = "# BEGIN Open-Inspect runtime assets"
	excludeEndMarker   = "# END Open-Inspect runtime assets"
)

// runtimeGitExcludes reads the exact repository-relative paths the sandbox
// runtime installed into the checkout's info/exclude. Untracked files under
// those paths are runtime assets, not the agent's work, and are left out of the
// capture. Port of read_runtime_git_excludes in git_excludes.py; the install
// side belongs to the un-ported boot orchestrator.
func (c *collector) runtimeGitExcludes(ctx context.Context, dir string) map[string]bool {
	raw, err := c.runGit(ctx, dir, gitOptions{maxStdout: 4096}, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return nil
	}
	path := strings.TrimSpace(string(raw))
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return managedRuntimePaths(string(contents))
}

// managedRuntimePaths extracts the rooted patterns inside the managed block.
func managedRuntimePaths(contents string) map[string]bool {
	lines := strings.Split(contents, "\n")
	start, end := -1, -1
	for i, line := range lines {
		switch {
		case line == excludeBeginMarker && start < 0:
			start = i
		case line == excludeEndMarker && start >= 0:
			end = i
		}
		if end >= 0 {
			break
		}
	}
	if start < 0 || end < 0 {
		return nil
	}

	paths := map[string]bool{}
	for _, pattern := range lines[start+1 : end] {
		if !strings.HasPrefix(pattern, "/") {
			continue
		}
		path := pattern[1:]
		if rooted, ok := rootedPattern(path); ok && rooted == pattern {
			paths[path] = true
		}
	}
	return paths
}

// rootedPattern renders a repository-relative path as the anchored gitignore
// pattern the runtime writes, rejecting absolute or escaping paths.
func rootedPattern(path string) (string, bool) {
	trailingSlash := strings.HasSuffix(path, "/")
	cleaned := strings.TrimSuffix(path, "/")
	if cleaned == "" || cleaned == "." || strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	for part := range strings.SplitSeq(cleaned, "/") {
		if part == ".." {
			return "", false
		}
	}
	normalized := filepath.ToSlash(filepath.Clean(cleaned))
	if normalized != cleaned {
		return "", false
	}
	if trailingSlash {
		return "/" + normalized + "/", true
	}
	return "/" + normalized, true
}

// isRuntimeExcluded reports whether a repository-relative path is covered by
// runtime ownership.
func isRuntimeExcluded(path string, runtimePaths map[string]bool) bool {
	for runtimePath := range runtimePaths {
		trimmed := strings.TrimSuffix(runtimePath, "/")
		if path == trimmed || strings.HasPrefix(path, trimmed+"/") {
			return true
		}
	}
	return false
}
