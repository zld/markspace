package media

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// SampleDescription carries the fields needed to describe an LPCM/MP4 audio
// sample entry: channel layout, sample depth and fixed-point 16.16 rate.
type SampleDescription struct {
	Channels   uint16
	BitsPer    uint16
	SampleRate uint32
}

// KeyframeIndex is the sync-sample table ("stss") for one deliverable
// segment. Every entry is a 1-based sample number inside the segment.
type KeyframeIndex struct {
	SampleNumbers []uint32
}

// MP4Artifact is the deterministic, in-memory MP4 skeleton produced for a
// deliverable segment. It contains the sample description and keyframe index
// but no media data, matching the kernel's structural scope.
type MP4Artifact struct {
	Description SampleDescription
	Keyframes   KeyframeIndex
	BoxBytes    int // serialized size of the generated moov skeleton
}

// memWriteSeeker is an in-memory io.WriteSeeker. Writes at a position inside
// the existing buffer patch bytes in place (used when EndBox rewrites box
// sizes); writes at the end extend the buffer.
type memWriteSeeker struct {
	buf []byte
	pos int64
}

func (m *memWriteSeeker) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if end > int64(len(m.buf)) {
		m.buf = append(m.buf, make([]byte, end-int64(len(m.buf)))...)
	}
	copy(m.buf[m.pos:end], p)
	m.pos = end
	return len(p), nil
}

func (m *memWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = m.pos + offset
	case io.SeekEnd:
		target = int64(len(m.buf)) + offset
	}
	if target < 0 {
		return 0, errors.New("negative seek position")
	}
	m.pos = target
	return target, nil
}

func (m *memWriteSeeker) Bytes() []byte { return m.buf }

// BuildMP4Artifact serializes an ftyp + moov skeleton using go-mp4, reads the
// tables back to prove the round trip, and returns the structured artifact.
// It fails instead of emitting a partial skeleton when a table is invalid.
func BuildMP4Artifact(desc SampleDescription, keyframes KeyframeIndex) (*MP4Artifact, error) {
	if desc.Channels == 0 || desc.BitsPer == 0 || desc.SampleRate == 0 {
		return nil, errors.New("sample description must have non-zero channels, depth and rate")
	}
	for i, n := range keyframes.SampleNumbers {
		if n == 0 {
			return nil, fmt.Errorf("keyframe entry %d is zero: stss sample numbers are 1-based", i)
		}
	}

	mem := &memWriteSeeker{}
	w := mp4.NewWriter(mem)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:       [4]byte{'M', '4', 'A', ' '},
		MinorVersion:     0,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return nil, err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{FullBox: mp4.FullBox{}, EntryCount: 1}, mp4.Context{}); err != nil {
		return nil, err
	}
	// The mp4a entry is a nested box and needs its own 8-byte box header
	// before the AudioSampleEntry payload.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMp4a()}); err != nil {
		return nil, err
	}
	entry := &mp4.AudioSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:        mp4.AnyTypeBox{Type: mp4.BoxTypeMp4a()},
			DataReferenceIndex: 1,
		},
		ChannelCount: desc.Channels,
		SampleSize:   desc.BitsPer,
		SampleRate:   desc.SampleRate << 16, // fixed-point 16.16
	}
	if _, err := mp4.Marshal(w, entry, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // mp4a
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // stsd
		return nil, err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
		return nil, err
	}
	stss := &mp4.Stss{
		EntryCount:   uint32(len(keyframes.SampleNumbers)),
		SampleNumber: append([]uint32(nil), keyframes.SampleNumbers...),
	}
	if _, err := mp4.Marshal(w, stss, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // stss
		return nil, err
	}

	for i := 0; i < 5; i++ { // stbl, minf, mdia, trak, moov
		if _, err := w.EndBox(); err != nil {
			return nil, err
		}
	}

	raw := mem.Bytes()
	got, err := readBackKeyframes(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("mp4 round trip failed: %w", err)
	}
	if len(got) != len(keyframes.SampleNumbers) {
		return nil, fmt.Errorf("mp4 round trip mismatch: got %d keyframes, wrote %d", len(got), len(keyframes.SampleNumbers))
	}
	for i := range got {
		if got[i] != keyframes.SampleNumbers[i] {
			return nil, fmt.Errorf("mp4 round trip mismatch at %d: got %d want %d", i, got[i], keyframes.SampleNumbers[i])
		}
	}
	return &MP4Artifact{Description: desc, Keyframes: keyframes, BoxBytes: len(raw)}, nil
}

// readBackKeyframes walks the serialized boxes with ReadBoxStructure and
// extracts the stss sample numbers without assuming a fixed byte layout.
func readBackKeyframes(r io.ReadSeeker) ([]uint32, error) {
	var out []uint32
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type.MatchWith(mp4.BoxTypeStss()) {
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if stss, ok := box.(*mp4.Stss); ok {
				out = append(out, stss.SampleNumber...)
			}
			return box, nil
		}
		return h.Expand()
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		return nil, err
	}
	return out, nil
}
