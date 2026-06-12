package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jehiah/background_agents_bridge/internal/config"
	"github.com/jehiah/background_agents_bridge/internal/controlplane"
)

// GitCredential implements the git credential-helper protocol
// (https://git-scm.com/docs/gitcredentials#_custom_helpers). git invokes the
// helper with the operation as the final argument.
//
// Only "get" does anything: it brokers a fresh SCM token from the control plane
// for each git operation. Unlike the shell helper it replaces, it never caches —
// every get refetches. "store"/"erase"/unknown are no-ops (exit 0).
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

	cfg := config.Resolve(config.Flags{})
	c, err := controlplane.New(cfg.ControlPlaneURL, cfg.AuthToken, cfg.SessionID)
	if err != nil {
		return fmt.Errorf("git-credential: %w", err)
	}

	creds, err := c.SCMCredentials(context.Background(), host)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(stdout, "username=%s\n", creds.Username); err != nil {
		return err
	}
	if creds.Password != "" {
		if _, err := fmt.Fprintf(stdout, "password=%s\n", creds.Password); err != nil {
			return err
		}
	}
	return nil
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
