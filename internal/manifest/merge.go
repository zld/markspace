// Package manifest builds the deterministic publish manifest. Entries are
// keyed by stable task keys, always serialized in sorted order and rendered
// as fixed-field text lines, so the same input set produces byte-identical
// manifests across reruns and incremental merges never duplicate a key.
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"regress/internal/model"
)

// magicHeader is the fixed first line of every manifest file.
const magicHeader = "# regress manifest v1"

// Entry is one committed manifest row. Key is the stable task key and
// ContentSha is the hash of the artifact it points at.
type Entry struct {
	Key       string     `json:"key"`
	Track     string     `json:"track"`
	Kind      model.TrackKind `json:"kind"`
	Tick      int64      `json:"tick"`
	Size      int64      `json:"size"`
	ContentSha string    `json:"content_sha256"`
}

// line renders one row as a tab-separated, fixed-field text line. The format
// contains no timestamp and no map iteration, so it is fully deterministic.
func (e Entry) line() string {
	return strings.Join([]string{
		e.Key, e.Track, string(e.Kind),
		fmt.Sprintf("%d", e.Tick),
		fmt.Sprintf("%d", e.Size),
		e.ContentSha,
	}, "\t")
}

// Render serializes entries into manifest bytes: fixed header, blank-free
// body sorted by Key, trailing newline.
func Render(entries []Entry) []byte {
	sorted := append([]Entry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Key != sorted[j].Key {
			return sorted[i].Key < sorted[j].Key
		}
		if sorted[i].Tick != sorted[j].Tick {
			return sorted[i].Tick < sorted[j].Tick
		}
		return sorted[i].ContentSha < sorted[j].ContentSha
	})
	var b bytes.Buffer
	b.WriteString(magicHeader)
	b.WriteByte('\n')
	for _, e := range sorted {
		b.WriteString(e.line())
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// Sha256 returns the content hash of the rendered manifest bytes.
func Sha256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Merge combines an existing manifest (nil for a new one) with incoming
// entries. An incoming entry whose Key already exists replaces it only when
// its content hash changed; identical keys never appear twice. The result is
// returned sorted, ready for Render.
func Merge(existing []Entry, incoming []Entry) []Entry {
	byKey := make(map[string]Entry, len(existing)+len(incoming))
	order := make([]string, 0, len(existing)+len(incoming))
	put := func(e Entry) {
		if _, seen := byKey[e.Key]; !seen {
			order = append(order, e.Key)
		}
		byKey[e.Key] = e
	}
	for _, e := range existing {
		put(e)
	}
	for _, e := range incoming {
		put(e)
	}
	out := make([]Entry, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Parse reads a manifest file previously written by Render. A missing file
// yields a nil slice and no error, matching the first-publish case.
func Parse(path string) ([]Entry, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, model.NewError(model.StageCommit, "manifest-read", path, "",
			0, err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 0 || lines[0] != magicHeader {
		return nil, model.NewError(model.StageCommit, "manifest-magic", path, "",
			0, fmt.Errorf("%w: bad manifest header", model.ErrCheckpoint))
	}
	var entries []Entry
	for i, line := range lines[1:] {
		fields := strings.Split(line, "\t")
		if len(fields) != 6 {
			return nil, model.NewError(model.StageCommit, "manifest-row", path, "",
				int64(i + 1),
				fmt.Errorf("%w: %d fields", model.ErrCheckpoint, len(fields)))
		}
		var tick, size int64
		if _, err := fmt.Sscanf(fields[3], "%d", &tick); err != nil {
			return nil, model.NewError(model.StageCommit, "manifest-tick", path, "",
				int64(i + 1), fmt.Errorf("%w: %v", model.ErrCheckpoint, err))
		}
		if _, err := fmt.Sscanf(fields[4], "%d", &size); err != nil {
			return nil, model.NewError(model.StageCommit, "manifest-size", path, "",
				int64(i + 1), fmt.Errorf("%w: %v", model.ErrCheckpoint, err))
		}
		entries = append(entries, Entry{
			Key: fields[0], Track: fields[1],
			Kind: model.TrackKind(fields[2]), Tick: tick,
			Size: size, ContentSha: fields[5],
		})
	}
	return entries, nil
}

// WriteAtomic renders entries and writes them to path via a same-directory
// temp file plus rename, so readers never observe a half-written manifest.
func WriteAtomic(path string, entries []Entry) ([]byte, string, error) {
	data := Render(entries)
	sum := Sha256(data)
	tmp, err := os.CreateTemp(dirOf(path), ".manifest-*")
	if err != nil {
		return nil, "", model.NewError(model.StageCommit, "manifest-temp", path,
			"", 0, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = tmp.Close() }()
	if _, err := tmp.Write(data); err != nil {
		_ = os.Remove(tmpName)
		return nil, "", model.NewError(model.StageCommit, "manifest-write", path,
			"", 0, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = os.Remove(tmpName)
		return nil, "", model.NewError(model.StageCommit, "manifest-fsync", path,
			"", 0, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return nil, "", model.NewError(model.StageCommit, "manifest-rename", path,
			"", 0, err)
	}
	return data, sum, nil
}

func dirOf(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return "."
	}
	return path[:idx]
}
