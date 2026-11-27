// Package mp4index serializes the repaired random-access index into an
// ISOBMFF container using github.com/abema/go-mp4 and parses it back to prove
// a byte-level round trip preserves every key field.
//
// The container is deliberately minimal and self-describing:
//
//	ftyp                            file type
//	moov
//	  mvhd                          movie header (timescale/duration)
//	  trak
//	    tkhd                        track header
//	    mdia
//	      mdhd                      media header (timescale)
//	      hdlr                      video handler
//	      minf
//	        stbl
//	          stsd                  sample description (empty entry table)
//	          stts                  sample timing (one entry per sample)
//	          stss                  sync sample table (keyframes)
//	          stsz                  sample sizes
//	          stco                  chunk offsets (empty)
//	mdat                            index payload (canonical segment bytes)
package mp4index

import (
	"bytes"
	"errors"
	"io"

	"github.com/abema/go-mp4"

	"tsrepair/internal/mediaerr"
)

// Seek whence values, repeated here to avoid a bare import cycle surprise.
const (
	ioSeekStart   = io.SeekStart
	ioSeekCurrent = io.SeekCurrent
	ioSeekEnd     = io.SeekEnd
)

var errSeekOutOfRange = errors.New("mp4index: seek outside written region")

// Timescale is the MP4 media timescale used for index times: 90 kHz matches
// the PTS clock, so nanosecond times are scaled exactly.
const Timescale uint32 = 90_000

// Manifest is the in-memory index representation handed to serialization.
type Manifest struct {
	Representation  int
	Timescale       uint32
	Duration90k     uint64
	Samples         []SampleRecord
	KeyframeNumbers []uint32 // 1-based sample numbers of random-access points
	// SegmentCanonical holds each segment's canonical bytes, carried in mdat.
	SegmentCanonical [][]byte
}

// SampleRecord is one sample row in the index.
type SampleRecord struct {
	Delta90k uint32 // presentation delta from the previous sample
	Size     uint32
}

// Serialize writes an MP4 container describing m and returns its bytes.
func Serialize(m *Manifest) ([]byte, error) {
	var buf writeSeekerBuffer
	w := mp4.NewWriter(&buf)

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	if err := writeMoov(w, m); err != nil {
		return nil, err
	}

	// mdat carries the concatenated canonical segment bytes.
	var payload []byte
	for _, c := range m.SegmentCanonical {
		payload = append(payload, c...)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Mdat{Data: payload}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// writeSeekerBuffer adapts bytes.Buffer to io.WriteSeeker. Seek only supports
// positions within the already-written region, which is all go-mp4 needs for
// back-patching box sizes.
type writeSeekerBuffer struct {
	buf []byte
	pos int64
}

func (b *writeSeekerBuffer) Write(p []byte) (int, error) {
	end := b.pos + int64(len(p))
	if int(end) > cap(b.buf) {
		grown := make([]byte, end, end*2)
		copy(grown, b.buf[:b.pos])
		b.buf = grown
	}
	if int(end) > len(b.buf) {
		b.buf = b.buf[:end]
	}
	copy(b.buf[b.pos:end], p)
	b.pos = end
	return len(p), nil
}

func (b *writeSeekerBuffer) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case ioSeekStart:
		target = offset
	case ioSeekCurrent:
		target = b.pos + offset
	case ioSeekEnd:
		target = int64(len(b.buf)) + offset
	}
	if target < 0 || target > int64(len(b.buf)) {
		return 0, errSeekOutOfRange
	}
	b.pos = target
	return target, nil
}

func (b *writeSeekerBuffer) Bytes() []byte { return b.buf }

func writeMoov(w *mp4.Writer, m *Manifest) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Mvhd{
		Timescale:  m.Timescale,
		DurationV0: uint32(m.Duration90k),
		Rate:       0x00010000,
		Volume:     0x0100,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if err := writeTrak(w, m); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // trak
		return err
	}
	if _, err := w.EndBox(); err != nil { // moov
		return err
	}
	return nil
}

func writeTrak(w *mp4.Writer, m *Manifest) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Tkhd{
		TrackID: uint32(m.Representation + 1),
		Width:   0, Height: 0,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Mdhd{
		Timescale:  m.Timescale,
		DurationV0: uint32(m.Duration90k),
		Language:   [3]byte{'u', 'n', 'd'},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Hdlr{
		HandlerType: [4]byte{'v', 'i', 'd', 'e'},
		Name:        "tsrepair-index",
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if err := writeStbl(w, m); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return err
	}
	return nil
}

func writeStbl(w *mp4.Writer, m *Manifest) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}

	// stsd with zero entries is legal for an index-only track.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 0}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	// stts: one run per distinct sample delta is avoided; emit one entry per
	// sample so timing round-trips exactly, including VFR spacing.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	entries := make([]mp4.SttsEntry, len(m.Samples))
	for i, s := range m.Samples {
		entries[i] = mp4.SttsEntry{SampleCount: 1, SampleDelta: s.Delta90k}
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{EntryCount: uint32(len(entries)), Entries: entries}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	// stss: keyframe (sync) sample numbers, 1-based.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stss{EntryCount: uint32(len(m.KeyframeNumbers)), SampleNumber: m.KeyframeNumbers}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	// stsz: per-sample sizes, uniform-size flag left at zero.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return err
	}
	sizes := make([]uint32, len(m.Samples))
	for i, s := range m.Samples {
		sizes[i] = s.Size
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(len(sizes)), EntrySize: sizes}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	// stco: no real media chunks in an index-only container.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stco{EntryCount: 0}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	if _, err := w.EndBox(); err != nil { // stbl
		return err
	}
	return nil
}

// RoundTrip serializes m, parses the bytes back and verifies the key fields.
// Every mismatch is returned as a structured ROUNDTRIP_FIELD_MISMATCH error
// naming the differing field, so the caller can fail with a distinct code.
func RoundTrip(m *Manifest) error {
	wire, err := Serialize(m)
	if err != nil {
		return err
	}
	parsed, mdat, err := Parse(wire)
	if err != nil {
		return err
	}
	return compare(m, parsed, mdat)
}

// Parse reads an index container back into a Manifest plus the mdat payload.
func Parse(wire []byte) (*Manifest, []byte, error) {
	r := bytes.NewReader(wire)
	m := &Manifest{Timescale: Timescale}
	var mdat []byte

	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		switch v := box.(type) {
		case *mp4.Mdhd:
			m.Timescale = v.Timescale
			m.Duration90k = uint64(v.DurationV0)
		case *mp4.Tkhd:
			m.Representation = int(v.TrackID) - 1
		case *mp4.Stts:
			for _, e := range v.Entries {
				m.Samples = append(m.Samples, SampleRecord{Delta90k: e.SampleDelta})
			}
		case *mp4.Stss:
			m.KeyframeNumbers = append(m.KeyframeNumbers, v.SampleNumber...)
		case *mp4.Stsz:
			for i := range m.Samples {
				if i < len(v.EntrySize) {
					m.Samples[i].Size = v.EntrySize[i]
				}
			}
		case *mp4.Mdat:
			mdat = v.Data
		}
		return h.Expand()
	})
	if err != nil {
		return nil, nil, err
	}
	return m, mdat, nil
}

func compare(want *Manifest, got *Manifest, gotMDAT []byte) error {
	var diffs []string
	if got.Timescale != want.Timescale {
		diffs = append(diffs, "timescale")
	}
	if got.Duration90k != want.Duration90k {
		diffs = append(diffs, "duration")
	}
	if len(got.Samples) != len(want.Samples) {
		diffs = append(diffs, "sample_count")
	} else {
		for i := range want.Samples {
			if got.Samples[i].Delta90k != want.Samples[i].Delta90k {
				diffs = append(diffs, "stts_delta")
				break
			}
			if got.Samples[i].Size != want.Samples[i].Size {
				diffs = append(diffs, "stsz_size")
				break
			}
		}
	}
	if len(got.KeyframeNumbers) != len(want.KeyframeNumbers) {
		diffs = append(diffs, "stss_count")
	} else {
		for i := range want.KeyframeNumbers {
			if got.KeyframeNumbers[i] != want.KeyframeNumbers[i] {
				diffs = append(diffs, "stss_entry")
				break
			}
		}
	}
	var wantMDAT []byte
	for _, c := range want.SegmentCanonical {
		wantMDAT = append(wantMDAT, c...)
	}
	if !bytes.Equal(gotMDAT, wantMDAT) {
		diffs = append(diffs, "mdat_payload")
	}
	if len(diffs) > 0 {
		return mediaerr.Fatal(mediaerr.CodeRoundtripField, 0, 0,
			"round-trip field mismatch: "+joinDiffs(diffs),
			"fail verification with exit code 1")
	}
	return nil
}

func joinDiffs(d []string) string {
	out := ""
	for i, s := range d {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
