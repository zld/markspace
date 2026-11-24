package medpipe

import (
	"os"
	"path/filepath"
)

// fragmentSink is the executor's chunk sink: each confirmed chunk becomes one
// self-contained fMP4 fragment (moof+mdat) whose name derives from the first
// frame ordinal, so an interrupted run regenerates identical filenames.
type fragmentSink struct {
	workDir string
	spec    TrackSpec
	idx     *TrackIndex
	resume  bool
}

// fragmentName renders the deterministic per-chunk output path.
func fragmentName(track string, firstOrdinal int) string {
	return track + ".frag-" + padOrdinal(firstOrdinal) + ".mp4"
}

func padOrdinal(n int) string {
	digits := []byte{'0', '0', '0', '0', '0', '0'}
	pos := len(digits) - 1
	for n > 0 && pos >= 0 {
		digits[pos] = byte('0' + n%10)
		n /= 10
		pos--
	}
	return string(digits)
}

// visit serializes one chunk and returns the payload bytes it wrote.
func (s *fragmentSink) visit(chunk []Frame) (int64, error) {
	firstOrd := chunk[0].Ordinal
	frag := Fragment{
		Sequence:     uint32(firstOrd + 1),
		TrackID:      1,
		DecodeTimeUS: uint64(s.idx.Entries[firstOrd].UnifiedUS),
	}
	var payloadSize uint64
	for _, f := range chunk {
		dur := s.sampleDuration(f.Ordinal)
		frag.Samples = append(frag.Samples, FragmentSample{
			DurationUS: uint32(dur),
			Size:       uint32(len(f.RawPayload)),
			IsKeyframe: f.Keyframe,
		})
		payloadSize += uint64(len(f.RawPayload))
	}
	name, perr := ResolveLocal(s.workDir, fragmentName(s.spec.Track, firstOrd))
	if perr != nil {
		return 0, perr
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return 0, err
	}
	// O_RDWR is required because WriteMoof patches the trun data offset by
	// re-reading the just-serialized moof; O_TRUNC makes a resumed run
	// overwrite the fragment at the same ordinal-based name, keeping output
	// sets identical across runs.
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if err := WriteMoof(f, frag, payloadSize); err != nil {
		return 0, err
	}
	var written int64
	for _, fr := range chunk {
		n, err := f.Write(fr.RawPayload)
		written += int64(n)
		if err != nil {
			return written, err
		}
	}
	if err := f.Sync(); err != nil {
		return written, err
	}
	return written, nil
}

// sampleDuration derives the unified-clock duration of one frame from the
// index time grid; the final frame reuses the previous delta.
func (s *fragmentSink) sampleDuration(ordinal int) int64 {
	entries := s.idx.Entries
	if ordinal+1 < len(entries) {
		return entries[ordinal+1].UnifiedUS - entries[ordinal].UnifiedUS
	}
	if ordinal > 0 {
		return entries[ordinal].UnifiedUS - entries[ordinal-1].UnifiedUS
	}
	return 0
}

// WriteTrackTFRA emits the ISO-BMFF random-access box sidecar for a track.
func WriteTrackTFRA(workDir, track string, trackID uint32, idx *TrackIndex) error {
	name, perr := ResolveLocal(workDir, track+".tfra")
	if perr != nil {
		return perr
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := WriteTFRA(f, trackID, idx); err != nil {
		return err
	}
	return f.Sync()
}
