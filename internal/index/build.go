// Package index builds the random-access index from manifest events and
// provides left-closed, right-open range reads over the unified ns timebase.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	mp4 "github.com/abema/go-mp4"

	"mediacheck/internal/manifest"
	"mediacheck/internal/timeline"
)

// Anomaly describes a corrupted interval discovered at build or verify time.
type Anomaly struct {
	Kind    timeline.Kind `json:"kind"`
	Source  string        `json:"source"`
	StartNs int64         `json:"start_ns"`
	EndNs   int64         `json:"end_ns"`
	Detail  string        `json:"detail"`
}

// Entry is one random-access index record with full provenance: source
// segment, raw-to-ns conversion inputs and the input digest.
type Entry struct {
	ID             int    `json:"id"`
	Representation string `json:"representation"`
	Seq            int64  `json:"seq"`
	ManifestPath   string `json:"manifest_path"`
	SegmentPath    string `json:"segment_path"`
	RawTime        uint64 `json:"raw_time"`
	RawDuration    uint64 `json:"raw_duration"`
	Timescale      uint64 `json:"timescale"`
	StartNs        int64  `json:"start_ns"`
	EndNs          int64  `json:"end_ns"`
	SampleOffset   int64  `json:"sample_offset"`
	SampleSize     uint64 `json:"sample_size"`
	SegmentDigest  string `json:"segment_digest"`
	Anomalous      bool   `json:"anomalous"`
}

// Index is the committed random-access index.
type Index struct {
	Entries      []Entry   `json:"entries"`
	Anomalies    []Anomaly `json:"anomalies"`
	MediaStartNs int64     `json:"media_start_ns"`
	MediaEndNs   int64     `json:"media_end_ns"`
}

// MapEvents converts events to ns-timed entries (the "map" stage). Timeline
// violations (overflow, backward time, zero timescale) abort with the
// structured error; nothing is silently corrected.
func MapEvents(events []manifest.Event) ([]Entry, error) {
	mapper := timeline.NewMapper()
	entries := make([]Entry, 0, len(events))
	for i, ev := range events {
		start, err := timeline.ToNs(ev.RawTime, ev.Timescale, ev.ManifestPath)
		if err != nil {
			return nil, err
		}
		dur, err := timeline.ToNs(ev.RawDuration, ev.Timescale, ev.ManifestPath)
		if err != nil {
			return nil, err
		}
		if err := mapper.Check(ev.Representation, start, start+dur, ev.ManifestPath, ev.Line); err != nil {
			return nil, err
		}
		entries = append(entries, Entry{
			ID: i, Representation: ev.Representation, Seq: ev.Seq,
			ManifestPath: ev.ManifestPath, SegmentPath: ev.SegmentPath,
			RawTime: ev.RawTime, RawDuration: ev.RawDuration, Timescale: ev.Timescale,
			StartNs: start, EndNs: start + dur,
		})
	}
	return entries, nil
}

// Assemble probes each segment for its random-access point and digest (the
// "build" stage). Unreadable or truncated segments are recorded as anomaly
// intervals on their entries instead of being dropped.
func Assemble(root string, entries []Entry) (*Index, error) {
	ix := &Index{Entries: entries}
	for i := range ix.Entries {
		e := &ix.Entries[i]
		full := filepath.Join(root, e.SegmentPath)
		d, derr := DigestFile(full)
		off, size, perr := ProbeSegment(full)
		switch {
		case derr != nil:
			e.Anomalous = true
			ix.Anomalies = append(ix.Anomalies, Anomaly{
				Kind: timeline.KindMissingRecord, Source: e.SegmentPath,
				StartNs: e.StartNs, EndNs: e.EndNs, Detail: derr.Error(),
			})
		case perr != nil:
			e.Anomalous = true
			e.SegmentDigest = d
			ix.Anomalies = append(ix.Anomalies, Anomaly{
				Kind: timeline.KindTruncated, Source: e.SegmentPath,
				StartNs: e.StartNs, EndNs: e.EndNs, Detail: perr.Error(),
			})
		default:
			e.SegmentDigest = d
			e.SampleOffset, e.SampleSize = off, size
		}
	}
	if len(ix.Entries) > 0 {
		ix.MediaStartNs, ix.MediaEndNs = ix.Entries[0].StartNs, ix.Entries[0].EndNs
		for _, e := range ix.Entries[1:] {
			if e.StartNs < ix.MediaStartNs {
				ix.MediaStartNs = e.StartNs
			}
			if e.EndNs > ix.MediaEndNs {
				ix.MediaEndNs = e.EndNs
			}
		}
	}
	return ix, nil
}

// ProbeSegment locates the first moof box (random access point) and the mdat
// payload size, validating that every box lies within the file.
func ProbeSegment(path string) (moofOffset int64, mdatSize uint64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	size := st.Size()
	moof := int64(-1)
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Offset+h.BoxInfo.Size > uint64(size) {
			return nil, fmt.Errorf("box %s at %d exceeds file size %d",
				h.BoxInfo.Type, h.BoxInfo.Offset, size)
		}
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoof():
			moof = int64(h.BoxInfo.Offset)
			return h.Expand()
		case mp4.BoxTypeMdat():
			mdatSize = h.BoxInfo.Size - h.BoxInfo.HeaderSize
		case mp4.BoxTypeMfhd(), mp4.BoxTypeTfhd(), mp4.BoxTypeTfdt(), mp4.BoxTypeTrun():
			if _, _, err := h.ReadPayload(); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("probe %s: %w", path, err)
	}
	if moof < 0 {
		return 0, 0, fmt.Errorf("probe %s: missing moof box", path)
	}
	return moof, mdatSize, nil
}

// DigestFile returns the sha256 digest of a file, prefixed with "sha256:".
func DigestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
