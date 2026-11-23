package manifest

import (
	"errors"
	"strings"

	"archivecheck/internal/model"
)

// DefaultTimebase is used for manifests that do not declare a timescale, such
// as HLS media playlists.
const DefaultTimebase int64 = 90000

// Options controls how a manifest is converted onto the shared timeline.
type Options struct {
	// Timebase forces the shared plan timebase. When zero, the parser derives
	// one: the LCM of DASH representation timescales, or DefaultTimebase for
	// HLS. A forced timebase that a representation timescale does not divide is
	// a hard error carrying the manifest source position.
	Timebase int64
	// AllowInexact keeps fractional plan ticks as an exact carried remainder
	// instead of rejecting the segment. The default (false) is strict: a
	// duration that is not an integral number of plan ticks is reported as a
	// CodeManifestTimebase fault with its source position.
	AllowInexact bool
	// MaxSegments bounds the number of segments per representation. Zero uses
	// DefaultMaxSegments; a negative value disables the bound.
	MaxSegments int
}

// DefaultMaxSegments caps expansion of an unbounded SegmentTimeline so a
// malformed @r cannot exhaust memory.
const DefaultMaxSegments = 200000

func (o Options) maxSegments() int {
	switch {
	case o.MaxSegments == 0:
		return DefaultMaxSegments
	case o.MaxSegments < 0:
		return -1
	default:
		return o.MaxSegments
	}
}

func (o Options) base(fallback int64) int64 {
	if o.Timebase > 0 {
		return o.Timebase
	}
	return fallback
}

// checkTimescales verifies that every declared media timescale divides base
// exactly, which is what makes a shared timeline lossless.
func (o Options) checkTimescales(base int64, timescales []int64) error {
	if base <= 0 {
		return model.NewFault(model.CodeManifestTimebase, model.SeverityError,
			model.Source{Layer: "manifest", Offset: -1}, "invalid shared timebase %d", base)
	}
	if err := model.CheckSharedTimebase(base, timescales); err != nil {
		return model.NewFault(model.CodeManifestTimebase, model.SeverityError,
			model.Source{Layer: "manifest", Offset: -1},
			"shared timebase %d cannot represent timescales %v: %s", base, timescales, err)
	}
	return nil
}

// isRemote reports whether a manifest URI must be rejected. The kernel only
// reads local files; any scheme with a host is treated as remote.
func isRemote(uri string) bool {
	lower := strings.ToLower(uri)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return true
	}
	// A scheme of the form "name://" is also non-local.
	if i := strings.Index(lower, "://"); i > 0 {
		return true
	}
	return false
}

// ErrRemoteURI is wrapped by faults for non-local manifest references.
var ErrRemoteURI = errors.New("remote URI is not supported")

// ValidateSimpleName accepts a manifest-provided resource name only when it is
// a relative, local path that cannot escape the fixture root.
func ValidateSimpleName(uri string) error {
	if uri == "" {
		return errors.New("empty resource name")
	}
	if isRemote(uri) {
		return ErrRemoteURI
	}
	if strings.HasPrefix(uri, "/") {
		return errors.New("absolute path is not supported")
	}
	for _, part := range strings.Split(uri, "/") {
		if part == ".." {
			return errors.New("path traversal is not supported")
		}
	}
	if strings.Contains(uri, "\\") {
		return errors.New("backslash separators are not supported")
	}
	return nil
}
