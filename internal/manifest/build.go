// Package manifest generates M3U8 media playlists and parses them back with
// one shared normalization path, so that generation and verification cannot
// disagree about durations or URIs.
package manifest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"streamcheck/internal/model"
)

// Version is the EXT-X-VERSION emitted by the builder.
const Version = 3

// Build renders a media playlist for one rate. The segment slice is emitted
// in the order given: callers supply playback order, and the verifier checks
// that order ascends. Numbers must be unique and non-negative.
func Build(rate int, segments []model.Segment) ([]byte, error) {
	if rate <= 0 {
		return nil, fmt.Errorf("rate must be positive, got %d", rate)
	}
	if len(segments) == 0 {
		return nil, errors.New("no segments to build")
	}
	if len(segments) > model.MaxSegments {
		return nil, fmt.Errorf("segment count %d exceeds limit %d", len(segments), model.MaxSegments)
	}
	seen := make(map[int]bool, len(segments))
	var maxDuration int64
	for _, seg := range segments {
		if seg.Number < 0 {
			return nil, fmt.Errorf("negative segment number %d", seg.Number)
		}
		if seen[seg.Number] {
			return nil, fmt.Errorf("duplicate segment number %d in rate %d", seg.Number, rate)
		}
		seen[seg.Number] = true
		if seg.DurationMS <= 0 {
			return nil, fmt.Errorf("segment %d: duration must be positive", seg.Number)
		}
		if seg.DurationMS > maxDuration {
			maxDuration = seg.DurationMS
		}
	}

	var buf bytes.Buffer
	buf.WriteString("#EXTM3U\n")
	fmt.Fprintf(&buf, "#EXT-X-VERSION:%d\n", Version)
	fmt.Fprintf(&buf, "#EXT-X-TARGETDURATION:%d\n", TargetDurationSeconds(maxDuration))
	fmt.Fprintf(&buf, "#EXT-X-MEDIA-SEQUENCE:%d\n", segments[0].Number)
	buf.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	for _, seg := range segments {
		uri, err := NormalizeURI(filepath.Base(seg.Path))
		if err != nil {
			return nil, fmt.Errorf("segment %d: %w", seg.Number, err)
		}
		fmt.Fprintf(&buf, "#EXTINF:%s,\n", FormatDurationMS(seg.DurationMS))
		buf.WriteString(uri)
		buf.WriteByte('\n')
	}
	buf.WriteString("#EXT-X-ENDLIST\n")
	return buf.Bytes(), nil
}

// TargetDurationSeconds is the HLS target duration: the ceiling of the
// longest segment duration, in whole seconds.
func TargetDurationSeconds(maxDurationMS int64) int64 {
	if maxDurationMS <= 0 {
		return 1
	}
	return (maxDurationMS + 999) / 1000
}

// FormatDurationMS renders a millisecond count with exactly three decimal
// places, which is the normalization the parser expects back.
func FormatDurationMS(ms int64) string {
	sign := ""
	if ms < 0 {
		sign = "-"
		ms = -ms
	}
	return fmt.Sprintf("%s%d.%03d", sign, ms/1000, ms%1000)
}

// ParseDurationMS parses <seconds>.<millis> with at most three fractional
// digits, without going through floating point.
func ParseDurationMS(text string) (int64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, errors.New("empty duration")
	}
	sign := int64(1)
	if strings.HasPrefix(text, "-") {
		sign = -1
		text = text[1:]
	}
	whole, frac, hasFrac := strings.Cut(text, ".")
	if whole == "" {
		return 0, fmt.Errorf("duration %q has no integer part", text)
	}
	secs, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("duration %q: %w", text, err)
	}
	var millis int64
	if hasFrac {
		if len(frac) == 0 || len(frac) > 3 {
			return 0, fmt.Errorf("duration %q must have 1-3 fractional digits", text)
		}
		for _, r := range frac {
			if r < '0' || r > '9' {
				return 0, fmt.Errorf("duration %q is not numeric", text)
			}
		}
		frac += strings.Repeat("0", 3-len(frac))
		millis, err = strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("duration %q: %w", text, err)
		}
	}
	return sign * (secs*1000 + millis), nil
}

// NormalizeURI validates a playlist URI and returns its canonical form: a
// bare base name with no directory component. Absolute paths, separators and
// traversal are rejected so a manifest can never point outside its directory.
func NormalizeURI(uri string) (string, error) {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return "", errors.New("empty URI")
	}
	if strings.ContainsAny(uri, "\x00\r\n") {
		return "", fmt.Errorf("URI %q contains control characters", uri)
	}
	if strings.Contains(uri, "/") || strings.Contains(uri, `\`) {
		return "", fmt.Errorf("URI %q must be a plain file name", uri)
	}
	if uri == "." || uri == ".." || strings.HasPrefix(uri, ".") {
		return "", fmt.Errorf("URI %q is not a media file name", uri)
	}
	if filepath.Base(uri) != uri {
		return "", fmt.Errorf("URI %q escapes its directory", uri)
	}
	return uri, nil
}

// WriteFileAtomic writes data to a sibling temporary file, syncs it and
// renames it into place. On cancellation or error the temporary file is
// removed, so an interrupted run never leaves a complete-looking playlist.
func WriteFileAtomic(ctx context.Context, path string, data []byte) error {
	return WriteManyAtomic(ctx, map[string][]byte{path: data})
}

// WriteManyAtomic stages every playlist beside its target and renames them
// only after all are written and synced. A cancellation or failure during
// staging leaves the previous files untouched and removes the staging files,
// so a multi-rate build is all-or-nothing per invocation.
func WriteManyAtomic(ctx context.Context, files map[string][]byte) error {
	if len(files) == 0 {
		return errors.New("no files to write")
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths) // deterministic staging order

	type staged struct {
		path string
		tmp  string
	}
	stagedFiles := make([]staged, 0, len(paths))
	cleanup := func() {
		for _, item := range stagedFiles {
			os.Remove(item.tmp)
		}
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			cleanup()
			return err
		}
		tmp, err := stage(filepath.Dir(path), path, files[path])
		if err != nil {
			cleanup()
			return err
		}
		stagedFiles = append(stagedFiles, staged{path: path, tmp: tmp})
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return err
	}
	for _, item := range stagedFiles {
		if err := os.Rename(item.tmp, item.path); err != nil {
			cleanup()
			return fmt.Errorf("rename %s: %w", item.path, err)
		}
	}
	return nil
}

// stage writes one file to a temporary sibling and returns its path.
func stage(dir, target string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, ".streamcheck-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp for %s: %w", target, err)
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return "", fmt.Errorf("write %s: %w", target, err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("sync %s: %w", target, err)
	}
	if err := f.Chmod(0o644); err != nil {
		return "", fmt.Errorf("chmod %s: %w", target, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", target, err)
	}
	ok = true
	return tmp, nil
}
