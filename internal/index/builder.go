package index

import (
	"encoding/binary"
	"io"
	"sort"

	"archive-recovery/internal/media"
)

// RandomAccessIndex is the rebuilt, append-only deterministic index. Only
// keyframes are random-access points; all frame entries are retained with
// their source ranges for manifest linkage.
type RandomAccessIndex struct {
	StreamSize int64
	Entries    []Entry
}

// Keyframes returns the random-access entries in offset order.
func (r RandomAccessIndex) Keyframes() []Entry {
	var out []Entry
	for _, e := range r.Entries {
		if e.IsKeyframe {
			out = append(out, e)
		}
	}
	return out
}

// Rebuild merges validated legacy entries with parsed frame events. Valid
// legacy entries are reused as-is (provenance marks their origin); invalid
// regions are rebuilt from parser output, never fabricated.
func Rebuild(li *LegacyIndex, statuses []EntryStatus, frames []media.FrameEvent, streamSize int64) RandomAccessIndex {
	validByOffset := map[int64]Entry{}
	if li != nil {
		for i, st := range statuses {
			if st.Valid {
				validByOffset[st.Entry.Offset] = li.Entries[i]
			}
		}
	}

	entries := make([]Entry, 0, len(frames))
	for seq, f := range frames {
		if !f.IsFrame {
			continue
		}
		if legacy, ok := validByOffset[f.Offset]; ok {
			entries = append(entries, legacy) // reused, keeps original record
			continue
		}
		entries = append(entries, Entry{
			FrameSeq:   uint32(seq),
			Offset:     f.Offset,
			Length:     f.Length,
			IsKeyframe: f.IsKeyframe,
		})
	}
	// Deterministic order regardless of legacy entry ordering.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Offset < entries[j].Offset })
	// Renumber frame sequences after merge so the rebuilt index is dense.
	for i := range entries {
		entries[i].FrameSeq = uint32(i)
	}
	return RandomAccessIndex{StreamSize: streamSize, Entries: entries}
}

// InvalidRegions summarizes contiguous runs of invalid legacy entries.
func InvalidRegions(statuses []EntryStatus) []string {
	var regions []string
	for _, st := range statuses {
		if !st.Valid {
			regions = append(regions, st.Reason)
		}
	}
	return regions
}

// Write serializes a rebuilt index using the same binary layout as the
// legacy format, so recovered and legacy files share one reader.
func (r RandomAccessIndex) Write(w io.Writer) error {
	var hdr [headerSize]byte
	copy(hdr[0:8], LegacyIndexMagic)
	binary.LittleEndian.PutUint16(hdr[8:10], LegacyIndexVersion)
	binary.LittleEndian.PutUint64(hdr[12:20], uint64(r.StreamSize))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	for _, e := range r.Entries {
		var raw [entrySize]byte
		binary.LittleEndian.PutUint32(raw[0:4], e.FrameSeq)
		binary.LittleEndian.PutUint64(raw[4:12], uint64(e.Offset))
		binary.LittleEndian.PutUint64(raw[12:20], uint64(e.Length))
		if e.IsKeyframe {
			raw[20] = 1
		}
		if _, err := w.Write(raw[:]); err != nil {
			return err
		}
	}
	return nil
}
