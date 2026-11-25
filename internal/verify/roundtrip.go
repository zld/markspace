// Package verify performs offline MP4 fragment round-trip validation and
// deterministic per-segment hashing. MP4 fixtures are generated and parsed
// with github.com/abema/go-mp4 v1.3.0; the round trip compares timescale,
// duration, track identity and keyframe (sync sample) indices between the
// written metadata and what the parser reads back. Corrupt input yields a
// located MediaError without affecting other representations.
package verify

import (
	"bytes"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediasync/internal/model"
)

// TrackSpec describes one track embedded in a generated MP4 fixture.
type TrackSpec struct {
	TrackID   uint32
	Timescale uint32
	Duration  uint64 // in timescale units
	Kind      model.AssetKind
	// KeyframeIndices are the 0-based sample indices that are sync samples.
	KeyframeIndices []uint32
}

// FragmentSpec describes one moof/mdat fragment with its base media decode
// time (tfdt) and sample entries (trun).
type FragmentSpec struct {
	TrackID        uint32
	BaseDecodeTime uint64
	SampleDuration uint32
	SampleCount    uint32
	Keyframe       bool
}

// MP4Metadata is the timing information extracted from a parsed MP4 stream.
type MP4Metadata struct {
	Tracks          []TrackSpec
	Fragments       []FragmentSpec
	FragmentOffsets []int64 // byte offset of each moof box
}

// WriteFragmentedMP4 serialises a deterministic fragmented MP4: a ftyp brand,
// a moov with one mdhd/tkhd per track and an mvex marker, followed by one
// moof/tfdt/trun + mdat pair per fragment.
func WriteFragmentedMP4(tracks []TrackSpec, frags []FragmentSpec) ([]byte, error) {
	buf := &bytes.Buffer{}
	w := mp4.NewWriter(newWriteSeeker(buf))

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '5'}},
		},
	}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	for _, tr := range tracks {
		if err := writeTrack(w, tr); err != nil {
			return nil, err
		}
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvex()}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // moov
		return nil, err
	}

	for _, fr := range frags {
		if err := writeFragment(w, fr); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func writeTrack(w *mp4.Writer, tr TrackSpec) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	// mdhd carries the timescale and duration that round-trip must preserve.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{Timescale: tr.Timescale, DurationV0: uint32(tr.Duration)}
	if _, err := mp4.Marshal(w, mdhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if tr.Kind == model.KindVideo && len(tr.KeyframeIndices) > 0 {
		if err := writeStss(w, tr.KeyframeIndices); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil { // stbl
		return err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return err
	}
	// tkhd carries the track id.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{TrackID: tr.TrackID, DurationV0: uint32(tr.Duration)}
	if _, err := mp4.Marshal(w, tkhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // trak
		return err
	}
	return nil
}

func writeStss(w *mp4.Writer, indices []uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
		return err
	}
	numbers := make([]uint32, len(indices))
	for i, idx := range indices {
		numbers[i] = idx + 1 // MP4 sample numbers are 1-based
	}
	if _, err := mp4.Marshal(w, &mp4.Stss{EntryCount: uint32(len(numbers)),
		SampleNumber: numbers}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	return nil
}

func writeFragment(w *mp4.Writer, fr FragmentSpec) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTfhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Tfhd{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}},
		TrackID: fr.TrackID}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTfdt()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Tfdt{BaseMediaDecodeTimeV0: uint32(fr.BaseDecodeTime)},
		mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrun()}); err != nil {
		return err
	}
	// sample-duration (0x100) and sample-size (0x200) present.
	trunFlags := [3]byte{0, 0x03, 0}
	entries := make([]mp4.TrunEntry, fr.SampleCount)
	for i := range entries {
		entries[i] = mp4.TrunEntry{SampleDuration: fr.SampleDuration, SampleSize: 8}
	}
	trun := &mp4.Trun{FullBox: mp4.FullBox{Flags: trunFlags},
		SampleCount: fr.SampleCount, Entries: entries}
	if _, err := mp4.Marshal(w, trun, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // moof
		return err
	}
	// mdat with deterministic placeholder payload.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return err
	}
	payload := bytes.Repeat([]byte{0xA5}, int(fr.SampleCount)*8)
	if _, err := w.Write(payload); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	return nil
}

// writeSeeker adapts a bytes.Buffer to io.WriteSeeker in memory.
type writeSeeker struct {
	buf *bytes.Buffer
	pos int64
}

func newWriteSeeker(buf *bytes.Buffer) *writeSeeker { return &writeSeeker{buf: buf} }

func (ws *writeSeeker) Write(p []byte) (int, error) {
	end := ws.pos + int64(len(p))
	if end > int64(ws.buf.Len()) {
		ws.buf.Write(make([]byte, end-int64(ws.buf.Len())))
	}
	copy(ws.buf.Bytes()[ws.pos:end], p)
	ws.pos = end
	return len(p), nil
}

func (ws *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = ws.pos + offset
	case io.SeekEnd:
		target = int64(ws.buf.Len()) + offset
	}
	if target < 0 || target > int64(ws.buf.Len()) {
		return 0, fmt.Errorf("seek out of range: %d", target)
	}
	ws.pos = target
	return target, nil
}

// ParseFragmentedMP4 reads a fragmented MP4 and extracts track timing
// (mdhd/tkhd), sync sample indices (stss) and per-fragment tfdt/trun timing.
// A truncated box (declared size runs past EOF) returns a located
// KindTruncatedMP4 error carrying the offending box's byte offset.
func ParseFragmentedMP4(data []byte) (*MP4Metadata, error) {
	if len(data) < 8 {
		return nil, model.NewError(model.KindTruncatedMP4, "verify", int64(len(data)),
			"input too short for any MP4 box: %d bytes", len(data))
	}
	if err := checkBoxBounds(data); err != nil {
		return nil, err
	}
	meta := &MP4Metadata{}
	var currentTrack *TrackSpec
	var currentFrag *FragmentSpec
	r := bytes.NewReader(data)
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMdhd():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			mdhd := box.(*mp4.Mdhd)
			currentTrack = &TrackSpec{Timescale: mdhd.Timescale, Duration: mdhd.GetDuration()}
			meta.Tracks = append(meta.Tracks, *currentTrack)
		case mp4.BoxTypeTkhd():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if len(meta.Tracks) > 0 {
				meta.Tracks[len(meta.Tracks)-1].TrackID = box.(*mp4.Tkhd).TrackID
			}
		case mp4.BoxTypeStss():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			stss := box.(*mp4.Stss)
			if len(meta.Tracks) > 0 {
				idx := make([]uint32, 0, len(stss.SampleNumber))
				for _, n := range stss.SampleNumber {
					idx = append(idx, n-1) // back to 0-based
				}
				meta.Tracks[len(meta.Tracks)-1].KeyframeIndices = idx
				meta.Tracks[len(meta.Tracks)-1].Kind = model.KindVideo
			}
		case mp4.BoxTypeMoof():
			currentFrag = &FragmentSpec{}
			meta.FragmentOffsets = append(meta.FragmentOffsets, int64(h.BoxInfo.Offset))
		case mp4.BoxTypeTfhd():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if currentFrag != nil {
				currentFrag.TrackID = box.(*mp4.Tfhd).TrackID
			}
		case mp4.BoxTypeTfdt():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if currentFrag != nil {
				currentFrag.BaseDecodeTime = box.(*mp4.Tfdt).GetBaseMediaDecodeTime()
			}
		case mp4.BoxTypeTrun():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			trun := box.(*mp4.Trun)
			if currentFrag != nil {
				currentFrag.SampleCount = trun.SampleCount
				if len(trun.Entries) > 0 {
					currentFrag.SampleDuration = trun.Entries[0].SampleDuration
				}
				meta.Fragments = append(meta.Fragments, *currentFrag)
				currentFrag = nil
			}
		}
		return h.Expand()
	})
	if err != nil {
		return nil, model.NewError(model.KindMalformedMP4, "verify", 0,
			"MP4 structure parse failed: %v", err)
	}
	return meta, nil
}

// checkBoxBounds walks top-level box headers and rejects any box whose
// declared size extends beyond the buffer, reporting its byte offset.
func checkBoxBounds(data []byte) error {
	pos := int64(0)
	for pos < int64(len(data)) {
		if int64(len(data))-pos < 8 {
			return model.NewError(model.KindTruncatedMP4, "verify", pos,
				"truncated box header at offset %d", pos)
		}
		size := int64(u32(data[pos : pos+4]))
		hdr := int64(8)
		if size == 1 {
			if int64(len(data))-pos < 16 {
				return model.NewError(model.KindTruncatedMP4, "verify", pos,
					"truncated 64-bit box header at offset %d", pos)
			}
			size = int64(u64(data[pos+8 : pos+16]))
			hdr = 16
		}
		if size == 0 {
			break // box extends to EOF by definition
		}
		if size < hdr {
			return model.NewError(model.KindMalformedMP4, "verify", pos,
				"box size %d smaller than header at offset %d", size, pos)
		}
		if pos+size > int64(len(data)) {
			return model.NewError(model.KindTruncatedMP4, "verify", pos,
				"box declares %d bytes but only %d remain at offset %d",
				size, int64(len(data))-pos, pos)
		}
		pos += size
	}
	return nil
}

// RoundTripResult reports one representation's round-trip comparison.
type RoundTripResult struct {
	Representation string
	OK             bool
	Reasons        []string
	Metadata       *MP4Metadata
}

// RoundTrip writes the spec to MP4, parses it back and compares timescale,
// duration, track id and keyframe/sample indices. A corrupt-input error is
// returned to the caller while other representations keep processing.
func RoundTrip(rep string, tracks []TrackSpec, frags []FragmentSpec) (RoundTripResult, error) {
	data, err := WriteFragmentedMP4(tracks, frags)
	if err != nil {
		return RoundTripResult{Representation: rep, OK: false},
			model.NewError(model.KindMalformedMP4, "verify", 0, "write failed: %v", err)
	}
	meta, err := ParseFragmentedMP4(data)
	if err != nil {
		return RoundTripResult{Representation: rep, OK: false}, err
	}
	res := compareTracksOnly(rep, tracks, meta)
	res = compareFragments(res, frags, meta)
	return res, nil
}

// compareTracksOnly validates parsed track metadata against the written spec.
func compareTracksOnly(rep string, tracks []TrackSpec, meta *MP4Metadata) RoundTripResult {
	res := RoundTripResult{Representation: rep, Metadata: meta, OK: true}
	if len(meta.Tracks) != len(tracks) {
		res.OK = false
		res.Reasons = append(res.Reasons, fmt.Sprintf(
			"track count: wrote %d read %d", len(tracks), len(meta.Tracks)))
	}
	for i, want := range tracks {
		if i >= len(meta.Tracks) {
			break
		}
		got := meta.Tracks[i]
		if got.Timescale != want.Timescale || got.Duration != want.Duration ||
			got.TrackID != want.TrackID {
			res.OK = false
			res.Reasons = append(res.Reasons, fmt.Sprintf(
				"track %d timing/id mismatch: wrote id=%d ts=%d dur=%d, read id=%d ts=%d dur=%d",
				i, want.TrackID, want.Timescale, want.Duration,
				got.TrackID, got.Timescale, got.Duration))
		}
		if !sameIndices(got.KeyframeIndices, want.KeyframeIndices) {
			res.OK = false
			res.Reasons = append(res.Reasons, fmt.Sprintf(
				"track %d keyframe index mismatch: wrote %v read %v",
				i, want.KeyframeIndices, got.KeyframeIndices))
		}
	}
	return res
}

// compareFragments appends fragment-level differences to an existing result.
func compareFragments(res RoundTripResult, frags []FragmentSpec, meta *MP4Metadata) RoundTripResult {
	if len(meta.Fragments) != len(frags) {
		res.OK = false
		res.Reasons = append(res.Reasons, fmt.Sprintf(
			"fragment count: wrote %d read %d", len(frags), len(meta.Fragments)))
	}
	for i, want := range frags {
		if i >= len(meta.Fragments) {
			break
		}
		got := meta.Fragments[i]
		if got.BaseDecodeTime != want.BaseDecodeTime || got.SampleCount != want.SampleCount ||
			got.SampleDuration != want.SampleDuration || got.TrackID != want.TrackID {
			res.OK = false
			res.Reasons = append(res.Reasons, fmt.Sprintf(
				"fragment %d timing mismatch: wrote tfdt=%d n=%d dur=%d, read tfdt=%d n=%d dur=%d",
				i, want.BaseDecodeTime, want.SampleCount, want.SampleDuration,
				got.BaseDecodeTime, got.SampleCount, got.SampleDuration))
		}
	}
	return res
}

func sameIndices(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func u32(b []byte) uint32 { return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]) }

func u64(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}
