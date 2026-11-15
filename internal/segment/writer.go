package segment

import (
	"fmt"
	"os"
	"path/filepath"

	"hlsforge/internal/timeline"
)

// GapMarker is written next to every unfilled part slot so operators and the
// verifier can see an explicit hole instead of a silently skipped range.
const GapMarker = "GAP\n"

// WrittenFile records one artifact the build placed on disk.
type WrittenFile struct {
	URI    string // output-relative path
	IsGap  bool
	Bytes  int
}

// Persist writes part files (and concatenated segment files) for one track.
// Writes go to a temp file and are atomically renamed, so a partially written
// build can never leave a half-written file visible to readers.
func Persist(outDir string, t *timeline.Track, layout *TrackSegments) ([]WrittenFile, error) {
	var written []WrittenFile
	for si := range layout.Segments {
		seg := &layout.Segments[si]
		var segmentBytes []byte
		segDir := filepath.Join(outDir, layout.SegmentDir)
		if err := os.MkdirAll(segDir, 0o755); err != nil {
			return nil, err
		}
		for pi := range seg.Parts {
			p := &seg.Parts[pi]
			partPath := filepath.Join(outDir, p.URI)
			if p.Gap {
				if err := atomicWrite(partPath+".gap", []byte(GapMarker)); err != nil {
					return nil, err
				}
				written = append(written, WrittenFile{URI: p.URI + ".gap", IsGap: true})
				continue
			}
			part := t.Parts[p.PartSeq-t.FromSeq]
			if part.Fragment == nil {
				return nil, fmt.Errorf("segment: part %d marked present but has no fragment", p.PartSeq)
			}
			raw := part.Fragment.Raw
			if err := atomicWrite(partPath, raw); err != nil {
				return nil, err
			}
			segmentBytes = append(segmentBytes, raw...)
			written = append(written, WrittenFile{URI: p.URI, Bytes: len(raw)})
		}
		// A segment with no usable content still keeps its time slot: write a
		// GAP marker instead of a media file rather than shifting later seqs.
		segPath := filepath.Join(outDir, seg.URI)
		if !seg.HasContent {
			if err := atomicWrite(segPath+".gap", []byte(GapMarker)); err != nil {
				return nil, err
			}
			written = append(written, WrittenFile{URI: seg.URI + ".gap", IsGap: true})
			continue
		}
		if err := atomicWrite(segPath, segmentBytes); err != nil {
			return nil, err
		}
		written = append(written, WrittenFile{URI: seg.URI, Bytes: len(segmentBytes)})
	}
	return written, nil
}

// atomicWrite writes data to a sibling temp file then renames it into place.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
