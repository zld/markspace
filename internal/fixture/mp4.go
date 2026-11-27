package fixture

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"
)

// memFile is an io.WriteSeeker over memory, used to size a box before it is
// written to disk. ISO BMFF requires the trun data offset to point past the
// moof, so the fragment header has to be measured first.
type memFile struct {
	buf []byte
	off int64
}

func (m *memFile) Write(p []byte) (int, error) {
	end := m.off + int64(len(p))
	if end > int64(len(m.buf)) {
		m.buf = append(m.buf, make([]byte, end-int64(len(m.buf)))...)
	}
	copy(m.buf[m.off:end], p)
	m.off = end
	return len(p), nil
}

func (m *memFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		m.off = offset
	case io.SeekCurrent:
		m.off += offset
	case io.SeekEnd:
		m.off = int64(len(m.buf)) + offset
	}
	if m.off < 0 {
		return 0, fmt.Errorf("fixture: negative seek")
	}
	return m.off, nil
}

func (m *memFile) Bytes() []byte { return m.buf }

// sampleSpec describes one sample inside a fragment.
type sampleSpec struct {
	Duration uint32
	Size     uint32
	Sync     bool
}

// fragmentSpec is the input for BuildFragment.
type fragmentSpec struct {
	Sequence   uint32
	TrackID    uint32
	BaseDecode uint64
	Timescale  uint32
	Samples    []sampleSpec
	Payload    []byte
	NonSync    bool // first sample is not a keyframe
}

// BuildInit writes a minimal initialization segment: ftyp + moov with mvhd,
// tkhd, mdhd and hdlr. It is valid enough for go-mp4 to read the timescale and
// track id back, which is all the scanner needs.
func BuildInit(timescale uint32, trackID uint32, brand string) ([]byte, error) {
	mem := &memFile{}
	w := mp4.NewWriter(mem)
	if err := writeBox(w, mp4.BoxTypeFtyp(), &mp4.Ftyp{
		MajorBrand:       brand4("iso6"),
		MinorVersion:     0,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: brand4("iso6")}, {CompatibleBrand: brand4("dash")}},
	}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeMvhd(), &mp4.Mvhd{
		Timescale:   timescale,
		DurationV0:  0,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		NextTrackID: trackID + 1,
	}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeTkhd(), &mp4.Tkhd{
		TrackID: trackID,
		Matrix:  [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		Width:   640 << 16,
		Height:  360 << 16,
	}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeMdhd(), &mp4.Mdhd{
		Timescale: timescale,
		Language:  [3]byte{'u', 'n', 'd'},
	}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeHdlr(), &mp4.Hdlr{
		HandlerType: brand4("vide"),
		Name:        "MediaBatch Fixture",
	}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // trak
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // moov
		return nil, err
	}
	return mem.Bytes(), nil
}

// BuildFragment writes styp + moof + mdat. The trun data offset is computed
// from the measured moof size, so the fragment is structurally consistent and
// its mdat length matches the declared sample sizes.
func BuildFragment(spec fragmentSpec) ([]byte, error) {
	moof, err := buildMoof(spec)
	if err != nil {
		return nil, err
	}
	mem := &memFile{}
	w := mp4.NewWriter(mem)
	if err := writeBox(w, mp4.BoxTypeStyp(), &mp4.Styp{
		MajorBrand:       brand4("msdh"),
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: brand4("msdh")}, {CompatibleBrand: brand4("iso6")}},
	}); err != nil {
		return nil, err
	}
	if _, err := mem.Write(moof); err != nil {
		return nil, err
	}
	// mdat header + payload.
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(8+len(spec.Payload)))
	copy(hdr[4:], "mdat")
	if _, err := mem.Write(hdr[:]); err != nil {
		return nil, err
	}
	if _, err := mem.Write(spec.Payload); err != nil {
		return nil, err
	}
	return mem.Bytes(), nil
}

func buildMoof(spec fragmentSpec) ([]byte, error) {
	build := func(dataOffset int32) ([]byte, error) {
		mem := &memFile{}
		w := mp4.NewWriter(mem)
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
			return nil, err
		}
		mfhd := &mp4.Mfhd{SequenceNumber: spec.Sequence}
		if err := writeBox(w, mp4.BoxTypeMfhd(), mfhd); err != nil {
			return nil, err
		}
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
			return nil, err
		}
		tfhd := &mp4.Tfhd{TrackID: spec.TrackID}
		tfhd.Flags = [3]byte{0x02, 0x00, 0x00} // default-base-is-moof, no defaults
		if err := writeBox(w, mp4.BoxTypeTfhd(), tfhd); err != nil {
			return nil, err
		}
		tfdt := &mp4.Tfdt{}
		tfdt.Version = 1
		tfdt.BaseMediaDecodeTimeV1 = spec.BaseDecode
		if err := writeBox(w, mp4.BoxTypeTfdt(), tfdt); err != nil {
			return nil, err
		}
		trun := &mp4.Trun{SampleCount: uint32(len(spec.Samples))}
		trun.Flags = [3]byte{0x00, 0x03, 0x05} // data-offset | first-sample-flags | duration | size
		trun.DataOffset = dataOffset
		if spec.NonSync {
			trun.FirstSampleFlags = 0x00010000 // sample_is_non_sync_sample
		}
		for _, s := range spec.Samples {
			trun.Entries = append(trun.Entries, mp4.TrunEntry{
				SampleDuration: s.Duration,
				SampleSize:     s.Size,
			})
		}
		if err := writeBox(w, mp4.BoxTypeTrun(), trun); err != nil {
			return nil, err
		}
		if _, err := w.EndBox(); err != nil { // traf
			return nil, err
		}
		if _, err := w.EndBox(); err != nil { // moof
			return nil, err
		}
		return mem.Bytes(), nil
	}
	first, err := build(0)
	if err != nil {
		return nil, err
	}
	// The moof size does not depend on the data offset value, only its encoded
	// width, which is fixed at 32 bits.
	final, err := build(int32(len(first)) + 8)
	if err != nil {
		return nil, err
	}
	if len(final) != len(first) {
		return nil, fmt.Errorf("fixture: moof size changed between passes")
	}
	return final, nil
}

func writeBox(w *mp4.Writer, typ mp4.BoxType, payload mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, payload, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func brand4(s string) [4]byte {
	var b [4]byte
	copy(b[:], s)
	return b
}

// deterministicPayload returns size bytes derived from seed. The same seed
// always yields the same bytes, so cross-representation fixtures can share
// content exactly.
func deterministicPayload(seed string, size int) []byte {
	out := make([]byte, 0, size)
	for i := 0; len(out) < size; i++ {
		h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", seed, i)))
		out = append(out, h[:]...)
	}
	return out[:size]
}

// writeFile writes data, creating parent directories.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
