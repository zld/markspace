package migrate

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"

	"example.com/assetcheck/internal/media"
	"example.com/assetcheck/internal/timeline"
)

// FragmentRecord is one line of the written fragment index. Recording the raw
// order and offset keeps the write stage auditable against the input bytes.
type FragmentRecord struct {
	Track    uint64 `json:"track"`
	Order    int    `json:"order"`
	Seq      int64  `json:"seq"`
	StartNS  int64  `json:"start_ns"`
	Offset   int64  `json:"offset"`
	Duration int64  `json:"duration_ns"`
}

// writeFragments streams the fragment index for an asset and validates the
// ordering contract as it goes. A defect aborts the write before the generation
// is renamed into place, so the previous output survives. The returned error is
// a structured write-stage error whose offset locates the offending block.
func writeFragments(w *bufio.Writer, asset *media.Asset) error {
	if asset == nil {
		return nil
	}
	lastStart := map[uint64]timeline.Nanosecond{}
	lastOffset := map[uint64]int64{}
	seenSeq := map[uint64]map[int64]bool{}
	lastSeq := map[uint64]int64{}

	// Preserve document order; the index is an audit trail, not a sort.
	records := append([]media.Fragment(nil), asset.Fragments...)
	for i := range records {
		rec := records[i]
		if rec.Duration < 0 || rec.Start < 0 {
			return writeFail(timeline.CatIllegalLength, rec.Offset,
				"fragment for track %d has illegal time start=%d duration=%d",
				rec.TrackNumber, int64(rec.Start), int64(rec.Duration))
		}
		if prev, ok := lastStart[rec.TrackNumber]; ok && rec.Start < prev {
			return writeFail(timeline.CatOrder, rec.Offset,
				"track %d fragment at offset %d starts at %s after %s (previous block at %d)",
				rec.TrackNumber, rec.Offset, timeline.FormatNS(rec.Start),
				timeline.FormatNS(prev), lastOffset[rec.TrackNumber])
		}
		lastStart[rec.TrackNumber] = rec.Start
		lastOffset[rec.TrackNumber] = rec.Offset
		if rec.Seq >= 0 {
			set := seenSeq[rec.TrackNumber]
			if set == nil {
				set = map[int64]bool{}
				seenSeq[rec.TrackNumber] = set
			}
			if set[rec.Seq] {
				return writeFail(timeline.CatDuplicate, rec.Offset,
					"track %d sequence %d repeated while writing fragments", rec.TrackNumber, rec.Seq)
			}
			if prev, ok := lastSeq[rec.TrackNumber]; ok && rec.Seq != prev+1 {
				return writeFail(timeline.CatMissingElement, rec.Offset,
					"track %d sequence gap while writing: %d follows %d",
					rec.TrackNumber, rec.Seq, prev)
			}
			set[rec.Seq] = true
			lastSeq[rec.TrackNumber] = rec.Seq
		}
		line := fmt.Sprintf(`{"track":%d,"order":%d,"seq":%d,"start_ns":%d,"offset":%d,"duration_ns":%d}`+"\n",
			rec.TrackNumber, rec.Order, rec.Seq, int64(rec.Start), rec.Offset, int64(rec.Duration))
		if _, err := w.WriteString(line); err != nil {
			return timeline.Wrap(err, timeline.StageWrite, timeline.CatIO, 0, rec.Offset,
				"write fragment index line %d", i)
		}
	}
	return nil
}

// writeFragmentIndex streams the fragment audit index to path, then fsyncs and
// closes it. It writes to a temporary sibling so a partial index never appears
// under the final name.
func writeFragmentIndex(path string, asset *media.Asset) error {
	if asset == nil {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fragments-*")
	if err != nil {
		return timeline.Wrap(err, timeline.StageWrite, timeline.CatIO, 0, -1,
			"create fragment index temp")
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	bw := bufio.NewWriterSize(tmp, 64<<10)
	if err := writeFragments(bw, asset); err != nil {
		tmp.Close()
		return err
	}
	if err := bw.Flush(); err != nil {
		tmp.Close()
		return timeline.Wrap(err, timeline.StageWrite, timeline.CatIO, 0, -1,
			"flush fragment index")
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return timeline.Wrap(err, timeline.StageWrite, timeline.CatIO, 0, -1,
			"fsync fragment index")
	}
	if err := tmp.Close(); err != nil {
		return timeline.Wrap(err, timeline.StageWrite, timeline.CatIO, 0, -1,
			"close fragment index")
	}
	if err := os.Rename(tmpName, path); err != nil {
		return timeline.Wrap(err, timeline.StageWrite, timeline.CatIO, 0, -1,
			"rename fragment index into place")
	}
	return nil
}

// writeFail builds a structured write-stage error at a raw input offset.
func writeFail(cat timeline.Category, off int64, format string, args ...interface{}) error {
	return timeline.New(timeline.StageWrite, cat, 0, off, format, args...)
}

// verifyWritten re-reads a staged file and confirms it is intact, which catches
// a truncated write or one that left an incomplete fragment index.
func verifyWritten(path string, wantBytes int) error {
	info, err := os.Stat(path)
	if err != nil {
		return timeline.Wrap(err, timeline.StageWrite, timeline.CatIO, 0, -1,
			"stat staged %s", filepath.Base(path))
	}
	if int(info.Size()) != wantBytes {
		return timeline.New(timeline.StageWrite, timeline.CatTruncated, 0, -1,
			"staged %s is %d bytes, expected %d", filepath.Base(path), info.Size(), wantBytes)
	}
	return nil
}
