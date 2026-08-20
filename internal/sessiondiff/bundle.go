// Package sessiondiff captures the session's git working state and uploads it
// to the control plane's durable diff viewer.
//
// It is a Go port of diff_collector.py / diff_capture.py in the upstream
// packages/sandbox-runtime/src/sandbox_runtime. Three pieces:
//
//   - the collector compares each checkout against an immutable per-repository
//     baseline commit and produces one bounded patch per renderable file;
//   - the Worker coalesces refresh requests (one per terminal prompt, plus the
//     control plane's refresh_diff command) into a single collection that runs
//     only while no prompt is in flight;
//   - the Client PUTs the bundle to the control plane, treating 404 as "this
//     control plane predates the diff viewer" and going quiet for good.
//
// Git is always invoked with an argument array; repository paths and filenames
// are never interpolated into a shell command.
package sessiondiff

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Capture ceilings, mirroring the DEFAULT_* constants in diff_collector.py.
const (
	DefaultMaxFiles         = 1000
	DefaultMaxPatchBytes    = 512 * 1024
	DefaultMaxCaptureBytes  = 1024 * 1024
	DefaultMaxBundleBytes   = 1_572_864
	DefaultMaxMetadataBytes = 8_000_000
	DefaultCommandTimeout   = 20 * time.Second

	// Version mirrors SESSION_DIFF_VERSION in the shared TypeScript types.
	Version = 1
	// maxErrorLength mirrors SESSION_DIFF_MAX_ERROR_LENGTH: the control plane
	// rejects longer error strings.
	maxErrorLength = 2000
)

// Render states, serialized verbatim.
const (
	renderRenderable   = "renderable"
	renderMetadataOnly = "metadata_only"
	renderTooLarge     = "too_large"
	renderBinary       = "binary"
)

// File statuses, serialized verbatim (mirrors diffFileStatusSchema).
const (
	statusAdded       = "added"
	statusModified    = "modified"
	statusDeleted     = "deleted"
	statusTypeChanged = "type_changed"
	statusUnmerged    = "unmerged"
	statusRenamed     = "renamed"
	statusSubmodule   = "submodule"
)

// ErrCapture marks a repository that could not produce a trustworthy capture.
// Mirrors DiffCaptureError; per-repository failures degrade to an "unavailable"
// bundle entry rather than failing the whole upload.
var ErrCapture = errors.New("session diff capture failed")

// Limits are the resource ceilings applied while collecting one bundle.
type Limits struct {
	MaxFiles         int
	MaxPatchBytes    int
	MaxCaptureBytes  int
	MaxBundleBytes   int
	MaxMetadataBytes int
	CommandTimeout   time.Duration
}

// DefaultLimits returns the production capture limits shared with the API
// contract.
func DefaultLimits() Limits {
	return Limits{
		MaxFiles:         DefaultMaxFiles,
		MaxPatchBytes:    DefaultMaxPatchBytes,
		MaxCaptureBytes:  DefaultMaxCaptureBytes,
		MaxBundleBytes:   DefaultMaxBundleBytes,
		MaxMetadataBytes: DefaultMaxMetadataBytes,
		CommandTimeout:   DefaultCommandTimeout,
	}
}

// Bundle is the wire representation uploaded atomically for all session
// repositories.
type Bundle struct {
	Version int `json:"version"`
	// TriggerMessageID is the prompt whose completion triggered the refresh, or
	// null for a control-plane-initiated one.
	TriggerMessageID *string `json:"triggerMessageId"`
	// CapturedAt is Unix milliseconds.
	CapturedAt   int64        `json:"capturedAt"`
	Repositories []Repository `json:"repositories"`
}

// Repository is one session repository's capture. Ready and unavailable
// entries share the struct; the optional fields carry the difference (a ready
// entry always reports truncated/omittedFileCount, an unavailable one only
// reports error).
type Repository struct {
	Status           string `json:"status"`
	Position         int    `json:"position"`
	RepoOwner        string `json:"repoOwner"`
	RepoName         string `json:"repoName"`
	BaseSHA          string `json:"baseSha"`
	HeadSHA          string `json:"headSha,omitempty"`
	Truncated        *bool  `json:"truncated,omitempty"`
	OmittedFileCount *int   `json:"omittedFileCount,omitempty"`
	Error            string `json:"error,omitempty"`
	Files            []File `json:"files"`
}

// File is one changed path within a repository capture.
type File struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Status string `json:"status"`
	// Additions and Deletions are null for binary content.
	Additions       *int   `json:"additions"`
	Deletions       *int   `json:"deletions"`
	RenderState     string `json:"renderState"`
	OldPath         string `json:"oldPath,omitempty"`
	Patch           string `json:"patch,omitempty"`
	OldMode         string `json:"oldMode,omitempty"`
	NewMode         string `json:"newMode,omitempty"`
	OldSubmoduleSHA string `json:"oldSubmoduleSha,omitempty"`
	NewSubmoduleSHA string `json:"newSubmoduleSha,omitempty"`
}

// encodeBundle produces the exact JSON bytes that are uploaded, which is also
// what the bundle limit is measured against.
//
// Unlike the Python encoder this does not escape non-ASCII (ensure_ascii): the
// wire is UTF-8 either way, and the size the limit acts on is the size actually
// sent.
func encodeBundle(bundle *Bundle) []byte {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(bundle); err != nil {
		// Bundle holds only strings, numbers and bools; encoding cannot fail.
		return nil
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// boundEncodedBundle sheds patches (largest first), then trailing metadata
// records, until the encoded bundle fits. Mirrors _bound_encoded_bundle.
func boundEncodedBundle(bundle *Bundle, maxBundleBytes int) error {
	// Index every patch-bearing file, largest patch first.
	type patchRef struct {
		size int
		repo int
		file int
	}
	var patches []patchRef
	for r := range bundle.Repositories {
		if bundle.Repositories[r].Status != "ready" {
			continue
		}
		for f := range bundle.Repositories[r].Files {
			if patch := bundle.Repositories[r].Files[f].Patch; patch != "" {
				patches = append(patches, patchRef{size: len(patch), repo: r, file: f})
			}
		}
	}
	// Largest patch first, stably, so equal-sized patches shed in bundle order.
	slices.SortStableFunc(patches, func(a, b patchRef) int { return b.size - a.size })

	for _, ref := range patches {
		if len(encodeBundle(bundle)) <= maxBundleBytes {
			return nil
		}
		file := &bundle.Repositories[ref.repo].Files[ref.file]
		file.Patch = ""
		file.RenderState = renderTooLarge
	}
	if len(encodeBundle(bundle)) <= maxBundleBytes {
		return nil
	}

	// Still over budget on metadata alone: drop trailing files, last repository
	// first, so the primary checkout keeps as much as possible.
	for r := len(bundle.Repositories) - 1; r >= 0; r-- {
		repo := &bundle.Repositories[r]
		if repo.Status != "ready" {
			continue
		}
		for len(repo.Files) > 0 && len(encodeBundle(bundle)) > maxBundleBytes {
			repo.Files = repo.Files[:len(repo.Files)-1]
			truncated, omitted := true, 0
			if repo.OmittedFileCount != nil {
				omitted = *repo.OmittedFileCount
			}
			omitted++
			repo.Truncated = &truncated
			repo.OmittedFileCount = &omitted
		}
	}
	if len(encodeBundle(bundle)) > maxBundleBytes {
		return errorf("Session diff metadata exceeded the bundle limit")
	}
	return nil
}
