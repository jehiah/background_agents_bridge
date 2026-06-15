package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jehiah/background_agents_bridge/internal/config"
	"github.com/jehiah/background_agents_bridge/internal/controlplane"
)

// credCacheFile is the on-disk cache of brokered SCM credentials, keyed by host.
// Located under the user's home so multiple bridge invocations (one per git op)
// share it within a single sandbox.
const credCacheFile = ".credentials/scm-creds.json"

// credExpiryBuffer is subtracted from each entry's expiry so we refresh before
// the token actually dies — a git push that starts with 5s left would otherwise
// race the server.
const credExpiryBuffer = 60 * time.Second

// GitCredential implements the git credential-helper protocol
// (https://git-scm.com/docs/gitcredentials#_custom_helpers). git invokes the
// helper with the operation as the final argument.
//
// "get" returns a cached credential when one exists and has not expired,
// otherwise it brokers a fresh one from the control plane and writes it to
// ~/.credentials/scm-creds.json for the next invocation. "store"/"erase"/unknown
// are no-ops (exit 0).
//
// stdin carries the request attributes git writes (protocol=, host=, path=, ...);
// stdout receives the credential lines.
func GitCredential(op string, stdin io.Reader, stdout io.Writer) error {
	if op != "get" {
		return nil
	}

	// When invoked by git, stdin is a pipe carrying key=value attrs terminated
	// by a blank line or EOF. When a human runs the command interactively from
	// a terminal there are no attrs to read; skip the scan so we don't block
	// waiting for the user to type a blank line.
	var attrs map[string]string
	if f, ok := stdin.(*os.File); ok && isTerminal(f) {
		attrs = map[string]string{}
	} else {
		attrs = parseCredentialAttrs(stdin)
	}
	host := attrs["host"]
	if host == "" {
		host = "github.com"
	}

	cachePath, _ := credCachePath()
	cache, _ := readCredCache(cachePath)
	if creds, ok := cache[host]; ok && !credExpired(creds, time.Now()) {
		_, err := creds.WriteTo(stdout)
		return err
	}

	cfg := config.Resolve(config.Flags{})
	c, err := controlplane.New(cfg.ControlPlaneURL, cfg.AuthToken, cfg.SessionID)
	if err != nil {
		return fmt.Errorf("git-credential: %w", err)
	}

	creds, err := c.SCMCredentials(context.Background(), host)
	if err != nil {
		return err
	}

	if cachePath != "" {
		if cache == nil {
			cache = map[string]controlplane.Credentials{}
		}
		cache[host] = creds
		_ = writeCredCache(cachePath, cache)
	}

	_, err = creds.WriteTo(stdout)
	return err
}

// credExpired reports whether creds are too close to (or past) their expiry to
// be safely reused. Entries with no expiry are treated as expired so we never
// keep an unbounded token in the cache.
func credExpired(creds controlplane.Credentials, now time.Time) bool {
	if creds.ExpiresAtEpochMs <= 0 {
		return true
	}
	expiry := time.UnixMilli(creds.ExpiresAtEpochMs)
	return !now.Before(expiry.Add(-credExpiryBuffer))
}

// credCachePath returns the absolute path to the on-disk cache file.
func credCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, credCacheFile), nil
}

// readCredCache loads the cache file. A missing/corrupt file yields an empty
// map and no error — callers refetch on cache miss anyway.
func readCredCache(path string) (map[string]controlplane.Credentials, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cache map[string]controlplane.Credentials
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}
	return cache, nil
}

// writeCredCache atomically writes the cache, mode 0600 (the file holds bearer
// tokens). Directory is created with 0700 if missing.
func writeCredCache(path string, cache map[string]controlplane.Credentials) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".scm-creds-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// parseCredentialAttrs reads git's key=value request lines until a blank line or
// EOF, returning them as a map.
func parseCredentialAttrs(r io.Reader) map[string]string {
	attrs := make(map[string]string)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		attrs[key] = value
	}
	_ = sc.Err() // git's request is small and well-formed; a read error just yields fewer attrs
	return attrs
}

// isTerminal reports whether f refers to a character device (a TTY). It avoids
// pulling in golang.org/x/term for what is a single Stat call.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
