package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/abema/go-mp4"
)

// memWriteSeeker is an in-memory io.WriteSeeker used to build boxes before
// they are copied to disk. go-mp4's Writer needs seeking to backfill sizes.
type memWriteSeeker struct {
	buf []byte
	pos int64
}

func (m *memWriteSeeker) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if end > int64(len(m.buf)) {
		grown := make([]byte, end)
		copy(grown, m.buf)
		m.buf = grown
	}
	copy(m.buf[m.pos:], p)
	m.pos = end
	return len(p), nil
}

func (m *memWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = m.pos + offset
	case io.SeekEnd:
		abs = int64(len(m.buf)) + offset
	default:
		return 0, fmt.Errorf("fixture: invalid whence %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("fixture: negative seek")
	}
	m.pos = abs
	return abs, nil
}

func (m *memWriteSeeker) Bytes() []byte { return m.buf }

// boxWriter wraps go-mp4's Writer for nested box construction.
type boxWriter struct {
	w *mp4.Writer
}

func newBoxWriter(ws io.WriteSeeker) *boxWriter { return &boxWriter{w: mp4.NewWriter(ws)} }

func (bw *boxWriter) start(typ string) error {
	_, err := bw.w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(typ)})
	return err
}

func (bw *boxWriter) end() error {
	_, err := bw.w.EndBox()
	return err
}

func (bw *boxWriter) full(typ string, version uint8, flags uint32, payload mp4.IBox) error {
	if err := bw.start(typ); err != nil {
		return err
	}
	payload.SetVersion(version)
	payload.SetFlags(flags)
	if _, err := mp4.Marshal(bw.w, payload, mp4.Context{}); err != nil {
		return err
	}
	return bw.end()
}

func marshalFtyp(dst io.Writer) (int, error) {
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '2'}},
			{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	}
	var payload bytes.Buffer
	n, err := mp4.Marshal(&payload, ftyp, mp4.Context{})
	if err != nil {
		return 0, err
	}
	if _, err := dst.Write(boxBytes("ftyp", payload.Bytes())); err != nil {
		return 0, err
	}
	return int(8 + n), nil
}

func ftypSize() (int, error) {
	var buf bytes.Buffer
	n, err := marshalFtyp(&buf)
	return n, err
}

// boxBytes frames payload with a small (8-byte) box header.
func boxBytes(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

// extraBoxes returns a known-ignorable free box and an unknown but legal
// box, exercising the scanner's skip path.
func extraBoxes() (freeBox, unknownBox []byte) {
	freeBox = boxBytes("free", []byte("archivist"))
	unknownBox = boxBytes("zzzz", make([]byte, 16))
	return
}

// footerSize is the byte count occupied by extraBoxes.
const footerSize = 16 + 24

// payloadByte is a deterministic pseudo-random byte for sample idx, offset i.
func payloadByte(idx int, i int) byte {
	x := uint32(idx)*0x9E3779B1 + uint32(i)*0x85EBCA77 + 0x27D4EB2F
	x ^= x >> 15
	x *= 0x2545F491
	x ^= x >> 13
	return byte(x)
}

// trackLayout is the concrete sample table the writer emits.
type trackLayout struct {
	ID        uint32
	MediaTS   uint32
	MovieTS   uint32
	Width     uint16
	Height    uint16
	DTS       []uint64
	PTSRaw    []uint32
	Sizes     []uint32
	Keyframes []uint32 // 1-based sample numbers
	ChunkOff  int64
}

func (t *trackLayout) sampleCount() int { return len(t.Sizes) }

// step returns the nominal decode delta.
func (t *trackLayout) step() uint64 {
	if len(t.DTS) < 2 {
		return 1
	}
	return t.DTS[1] - t.DTS[0]
}

func (t *trackLayout) duration() uint64 {
	if len(t.DTS) == 0 {
		return 0
	}
	return t.DTS[len(t.DTS)-1] + t.step()
}

func (t *trackLayout) ctts() []uint32 {
	out := make([]uint32, len(t.DTS))
	for i := range t.DTS {
		out[i] = t.PTSRaw[i] - uint32(t.DTS[i])
	}
	return out
}

func scaleUint(v uint64, from, to uint32) uint64 {
	if from == 0 || to == 0 || from == to {
		return v
	}
	// v*to/from with rounding; values here are far below 2^64/2^32.
	return (v*uint64(to) + uint64(from)/2) / uint64(from)
}

// rleSTTS emits one duration per sample. SampleCount therefore sums to the
// number of samples, and expanding the table reproduces every DTS exactly.
func rleSTTS(dts []uint64) []mp4.SttsEntry {
	if len(dts) == 0 {
		return []mp4.SttsEntry{}
	}
	deltas := make([]uint32, len(dts))
	for i := 0; i < len(dts)-1; i++ {
		deltas[i] = uint32(dts[i+1] - dts[i])
	}
	// The final sample keeps the nominal step so mdhd duration covers it.
	last := uint64(1)
	if len(dts) > 1 {
		last = dts[len(dts)-1] - dts[len(dts)-2]
	}
	if last == 0 {
		last = 1
	}
	deltas[len(deltas)-1] = uint32(last)

	var out []mp4.SttsEntry
	var count uint32
	var cur uint32
	flush := func() {
		if count > 0 {
			out = append(out, mp4.SttsEntry{SampleCount: count, SampleDelta: cur})
		}
	}
	for i, d := range deltas {
		if i == 0 {
			cur, count = d, 1
			continue
		}
		if d == cur {
			count++
			continue
		}
		flush()
		cur, count = d, 1
	}
	flush()
	return out
}

func rleCTTS(offsets []uint32) []mp4.CttsEntry {
	if len(offsets) == 0 {
		return nil
	}
	var out []mp4.CttsEntry
	var count uint32
	var cur uint32
	for i, v := range offsets {
		if i == 0 {
			cur, count = v, 1
			continue
		}
		if v == cur {
			count++
			continue
		}
		out = append(out, mp4.CttsEntry{SampleCount: count, SampleOffsetV0: cur})
		cur, count = v, 1
	}
	out = append(out, mp4.CttsEntry{SampleCount: count, SampleOffsetV0: cur})
	return out
}

func marshalMoov(t *trackLayout, movieTS uint32) ([]byte, error) {
	ws := &memWriteSeeker{}
	bw := newBoxWriter(ws)
	if err := bw.start("moov"); err != nil {
		return nil, err
	}
	if err := writeMvhd(bw, movieTS, t); err != nil {
		return nil, err
	}
	if err := writeTrak(bw, movieTS, t); err != nil {
		return nil, err
	}
	if err := bw.end(); err != nil {
		return nil, err
	}
	return ws.Bytes(), nil
}

func writeMvhd(bw *boxWriter, movieTS uint32, t *trackLayout) error {
	mvhd := &mp4.Mvhd{
		Timescale:   movieTS,
		DurationV1:  scaleUint(t.duration(), t.MediaTS, movieTS),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		NextTrackID: t.ID + 1,
	}
	return bw.full("mvhd", 1, 0, mvhd)
}

func writeTrak(bw *boxWriter, movieTS uint32, t *trackLayout) error {
	if err := bw.start("trak"); err != nil {
		return err
	}
	if err := writeTrakBody(bw, movieTS, t); err != nil {
		return err
	}
	return bw.end() // trak
}

func writeTrakBody(bw *boxWriter, movieTS uint32, t *trackLayout) error {
	tkhd := &mp4.Tkhd{
		TrackID:    t.ID,
		DurationV1: scaleUint(t.duration(), t.MediaTS, movieTS),
		Matrix:     [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		Width:      uint32(t.Width) << 16,
		Height:     uint32(t.Height) << 16,
	}
	if err := bw.full("tkhd", 1, 0x000003, tkhd); err != nil {
		return err
	}
	if err := bw.start("mdia"); err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{
		Timescale:  t.MediaTS,
		DurationV1: t.duration(),
		Language:   [3]byte{0x15, 0x0E, 0x04}, // "und"
	}
	if err := bw.full("mdhd", 1, 0, mdhd); err != nil {
		return err
	}
	hdlr := &mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "VideoHandler"}
	if err := bw.full("hdlr", 0, 0, hdlr); err != nil {
		return err
	}
	if err := writeMinf(bw, t); err != nil {
		return err
	}
	return bw.end() // mdia
}

func writeMinf(bw *boxWriter, t *trackLayout) error {
	if err := bw.start("minf"); err != nil {
		return err
	}
	if err := bw.full("vmhd", 0, 0x000001, &mp4.Vmhd{}); err != nil {
		return err
	}
	if err := bw.start("dinf"); err != nil {
		return err
	}
	if err := bw.start("dref"); err != nil {
		return err
	}
	if _, err := mp4.Marshal(bw.w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	url := &mp4.Url{}
	url.SetFlags(mp4.UrlSelfContained)
	if err := bw.start("url "); err != nil {
		return err
	}
	if _, err := mp4.Marshal(bw.w, url, mp4.Context{}); err != nil {
		return err
	}
	if err := bw.end(); err != nil {
		return err
	}
	if err := bw.end(); err != nil {
		return err
	}
	if err := bw.end(); err != nil {
		return err
	}
	if err := writeStbl(bw, t); err != nil {
		return err
	}
	return bw.end() // minf
}

func writeStbl(bw *boxWriter, t *trackLayout) error {
	if err := bw.start("stbl"); err != nil {
		return err
	}
	if err := bw.start("stsd"); err != nil {
		return err
	}
	if _, err := mp4.Marshal(bw.w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if err := bw.start("avc1"); err != nil {
		return err
	}
	entry := &mp4.VisualSampleEntry{
		SampleEntry:     mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.StrToBoxType("avc1")}, DataReferenceIndex: 1},
		Width:           t.Width,
		Height:          t.Height,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
		PreDefined3:     -1,
	}
	if _, err := mp4.Marshal(bw.w, entry, mp4.Context{}); err != nil {
		return err
	}
	// A minimal avcC makes the sample entry self-describing for container
	// tools. The payloads are synthetic, so decoders will still refuse to
	// produce frames; only structure is meant to be realistic.
	avcc := &mp4.AVCDecoderConfiguration{
		AnyTypeBox:                 mp4.AnyTypeBox{Type: mp4.StrToBoxType("avcC")},
		ConfigurationVersion:       1,
		Profile:                    66,
		ProfileCompatibility:       0xC0,
		Level:                      30,
		LengthSizeMinusOne:         3,
		NumOfSequenceParameterSets: 1,
		SequenceParameterSets: []mp4.AVCParameterSet{
			{Length: 8, NALUnit: []byte{0x67, 0x42, 0xC0, 0x1E, 0xD9, 0x00, 0xF0, 0x11}},
		},
		NumOfPictureParameterSets: 1,
		PictureParameterSets: []mp4.AVCParameterSet{
			{Length: 4, NALUnit: []byte{0x68, 0xCE, 0x38, 0x80}},
		},
	}
	// avcC is a child box inside the avc1 sample entry, so it needs its own
	// header; Marshal writes payload only.
	if err := bw.start("avcC"); err != nil {
		return err
	}
	if _, err := mp4.Marshal(bw.w, avcc, mp4.Context{}); err != nil {
		return err
	}
	if err := bw.end(); err != nil {
		return err
	}
	if err := bw.end(); err != nil {
		return err
	}
	if err := bw.end(); err != nil {
		return err
	}

	stts := &mp4.Stts{Entries: rleSTTS(t.DTS)}
	stts.EntryCount = uint32(len(stts.Entries))
	if err := bw.full("stts", 0, 0, stts); err != nil {
		return err
	}
	if entries := rleCTTS(t.ctts()); len(entries) > 0 {
		ctts := &mp4.Ctts{EntryCount: uint32(len(entries)), Entries: entries}
		if err := bw.full("ctts", 0, 0, ctts); err != nil {
			return err
		}
	}
	if len(t.Keyframes) > 0 && len(t.Keyframes) != len(t.Sizes) {
		stss := &mp4.Stss{EntryCount: uint32(len(t.Keyframes)), SampleNumber: t.Keyframes}
		if err := bw.full("stss", 0, 0, stss); err != nil {
			return err
		}
	}
	stsz := &mp4.Stsz{SampleCount: uint32(len(t.Sizes))}
	for _, s := range t.Sizes {
		stsz.EntrySize = append(stsz.EntrySize, s)
	}
	if err := bw.full("stsz", 0, 0, stsz); err != nil {
		return err
	}
	stsc := &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(len(t.Sizes)), SampleDescriptionIndex: 1}}}
	if err := bw.full("stsc", 0, 0, stsc); err != nil {
		return err
	}
	stco := &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{uint32(t.ChunkOff)}}
	if err := bw.full("stco", 0, 0, stco); err != nil {
		return err
	}
	return bw.end() // stbl
}

func mdatHeader(payloadLen int) ([]byte, error) {
	sz := uint64(payloadLen) + 8
	if sz > 0xFFFFFFFF {
		return nil, fmt.Errorf("fixture: mdat larger than 4GiB is unsupported")
	}
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[:4], uint32(sz))
	copy(hdr[4:], "mdat")
	return hdr, nil
}
