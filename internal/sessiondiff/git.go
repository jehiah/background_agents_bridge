package sessiondiff

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// errOutputTooLarge is returned when a git command exceeds its caller-provided
// stdout ceiling. Callers treat it as "this file/repository is too big",
// not as a failure. Mirrors _GitOutputTooLarge.
var errOutputTooLarge = errors.New("git output exceeded its limit")

// errorf builds a capture error carrying a user-visible message; the message
// is what the control plane displays for an unavailable repository.
func errorf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCapture, fmt.Sprintf(format, args...))
}

// captureMessage renders err for the wire: the ErrCapture prefix is an
// implementation detail, and the control plane bounds the length.
func captureMessage(err error) string {
	message := err.Error()
	message = strings.TrimPrefix(message, ErrCapture.Error()+": ")
	return truncate(message, maxErrorLength)
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	// Cut on a rune boundary so the wire value stays valid UTF-8.
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

// gitEnv is the environment every capture command runs with: no system config
// (so a repository-supplied external diff or textconv driver cannot execute),
// literal pathspecs (so a filename containing pathspec magic is treated as a
// name), no replace objects, no credential prompts, and a stable locale.
func gitEnv() []string {
	overrides := map[string]string{
		"GIT_CONFIG_NOSYSTEM":    "1",
		"GIT_LITERAL_PATHSPECS":  "1",
		"GIT_NO_REPLACE_OBJECTS": "1",
		"GIT_TERMINAL_PROMPT":    "0",
		"LC_ALL":                 "C",
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		if key, _, ok := strings.Cut(entry, "="); ok {
			if _, overridden := overrides[key]; overridden {
				continue
			}
		}
		env = append(env, entry)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

// gitOptions tunes one git invocation.
type gitOptions struct {
	// maxStdout caps stdout; exceeding it aborts the command with
	// errOutputTooLarge. Zero means unlimited.
	maxStdout int
	// acceptedCodes are exit codes treated as success (default: 0 only).
	// `git diff --no-index` uses 1 to mean "differences found".
	acceptedCodes []int
}

// runGit executes git in dir and returns its stdout.
func (c *collector) runGit(ctx context.Context, dir string, opts gitOptions, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.limits.CommandTimeout)
	defer cancel()

	stdout := &cappedBuffer{limit: opts.maxStdout}
	stderr := &cappedBuffer{limit: 64 * 1024}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = c.env
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if stdout.overflowed() {
		// The process was torn down by the closed pipe (or is about to be); its
		// exit status is meaningless next to the size verdict.
		return nil, errOutputTooLarge
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errorf("Git command timed out for %s", c.name)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && slices.Contains(acceptedCodes(opts), exitErr.ExitCode()) {
			return stdout.bytes(), nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		detail := strings.TrimSpace(decodeUTF8Replace(stderr.bytes()))
		if detail == "" {
			detail = err.Error()
		}
		return nil, errorf("Git command failed for %s: %s", c.name, detail)
	}
	return stdout.bytes(), nil
}

func acceptedCodes(opts gitOptions) []int {
	if len(opts.acceptedCodes) == 0 {
		return []int{0}
	}
	return opts.acceptedCodes
}

// cappedBuffer accumulates output up to limit bytes and then refuses more,
// which closes the pipe and stops the command. A zero limit is unlimited.
type cappedBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
	over  bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit > 0 && len(b.buf)+len(p) > b.limit {
		b.over = true
		return 0, errOutputTooLarge
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *cappedBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf
}

func (b *cappedBuffer) overflowed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.over
}

// decodeUTF8Replace decodes raw as UTF-8, replacing each invalid byte with
// U+FFFD — the same substitution Python's errors="replace" makes, so patch
// text and its byte length match the upstream capture.
func decodeUTF8Replace(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	var out strings.Builder
	out.Grow(len(raw))
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && size <= 1 {
			out.WriteRune(utf8.RuneError)
			i++
			continue
		}
		out.Write(raw[i : i+size])
		i += size
	}
	return out.String()
}

// randomUUID returns a version 4 UUID string, the per-file identity the
// control plane keys renders on.
func randomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any supported platform.
		panic("sessiondiff: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(b[:])
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:]
}
