package ingest

import (
	"bytes"
	"testing"

	"mediad/internal/model"
)

// aacPayload builds an RFC 3640 payload: AU headers (13-bit size, 3-bit idx)
// followed by the given frame byte counts.
func aacPayload(frameSizes ...int) []byte {
	var hdr, data bytes.Buffer
	for _, sz := range frameSizes {
		word := uint16(sz)<<3 | 0
		hdr.WriteByte(byte(word >> 8))
		hdr.WriteByte(byte(word))
		data.Write(bytes.Repeat([]byte{0xAB}, sz))
	}
	out := []byte{byte(hdr.Len() * 8 >> 8), byte(hdr.Len() * 8)}
	out = append(out, hdr.Bytes()...)
	return append(out, data.Bytes()...)
}

func TestAACDurationFromAUHeader(t *testing.T) {
	spec := TrackSpec{ID: "aac-a", Codec: CodecAAC, ClockRate: 48000, AACSampleRate: 48000}
	raw := marshalRTP(3, 500, aacPayload(10, 10))
	pkt, err := ParseRTP(spec, raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := Parse(spec, pkt); err != nil {
		t.Fatal(err)
	}
	// 2 frames * 1024 samples at 48 kHz.
	wantNS := model.Nano(2) * 1024 * model.Second / 48000
	if pkt.DurationNS != wantNS {
		t.Fatalf("DurationNS=%d want %d", pkt.DurationNS, wantNS)
	}
}

func TestAACTruncationAndMismatch(t *testing.T) {
	spec := TrackSpec{ID: "aac-a", Codec: CodecAAC, ClockRate: 48000, AACSampleRate: 48000}
	bad := [][]byte{
		{0x00},                         // no AU-headers-length
		{0x00, 0x10, 0x00},             // header section extends past payload
		{0x00, 0x10, 0x20, 0x00, 0xAA}, // declared AU size 256, one data byte
	}
	for i, pl := range bad {
		raw := marshalRTP(uint16(30+i), 0, pl)
		pkt, err := ParseRTP(spec, raw, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := Parse(spec, pkt); model.AsError(err).Code != model.ErrAACHeader {
			t.Fatalf("case %d: want ErrAACHeader, got %v", i, err)
		}
	}
}

// box builds a length-prefixed MP4 box from a fourcc and payload.
func box(fourcc string, payload []byte) []byte {
	var b bytes.Buffer
	size := uint32(8 + len(payload))
	b.WriteByte(byte(size >> 24))
	b.WriteByte(byte(size >> 16))
	b.WriteByte(byte(size >> 8))
	b.WriteByte(byte(size))
	b.WriteString(fourcc)
	b.Write(payload)
	return b.Bytes()
}

func fullBox(ver byte, flags uint32) []byte {
	return []byte{ver, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

func u32(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

// tinyMP4 builds moov/trak/mdia(mdhd)/minf/stbl(stts,stsz) by hand so the
// read path can be exercised without external media files.
func tinyMP4(timescale uint32, sampleCount, delta, constSize uint32) []byte {
	mdhd := append(fullBox(0, 0), append(u32(0), u32(0)...)...) // creation, modification
	mdhd = append(mdhd, u32(timescale)...)
	mdhd = append(mdhd, u32(delta*sampleCount)...) // duration
	mdhd = append(mdhd, 0x55, 0xC4, 0x00, 0x00)    // language 'und' + pre-defined
	stts := append(fullBox(0, 0), u32(1)...)
	stts = append(stts, u32(sampleCount)...)
	stts = append(stts, u32(delta)...)
	stsz := append(fullBox(0, 0), u32(constSize)...)
	stsz = append(stsz, u32(sampleCount)...)
	stbl := append(box("stts", stts), box("stsz", stsz)...)
	minf := box("minf", stbl)
	mdia := append(box("mdhd", mdhd), minf...)
	trak := box("trak", box("mdia", mdia))
	return append(box("ftyp", []byte("isom\x00\x00\x00\x00isom")), box("moov", trak)...)
}

func TestReadMP4TimeBase(t *testing.T) {
	raw := tinyMP4(48000, 4, 1024, 97)
	tb, err := ReadMP4TimeBase(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if tb.Timescale != 48000 || len(tb.SampleDeltas) != 4 || len(tb.SampleSizes) != 4 {
		t.Fatalf("unexpected time base: %+v", tb)
	}
	if tb.SampleDeltas[0] != 1024 || tb.SampleSizes[2] != 97 {
		t.Fatalf("sample entries wrong: %+v", tb)
	}
	ns, err := tb.SampleNanos(1024)
	if err != nil || ns != 1024*model.Second/48000 {
		t.Fatalf("SampleNanos = %d, %v", ns, err)
	}
}

func TestReadMP4TimeBaseMissingBoxes(t *testing.T) {
	// ftyp alone: no mdhd/stts at all.
	_, err := ReadMP4TimeBase(bytes.NewReader(box("ftyp", []byte("isom\x00\x00\x00\x00isom"))))
	if model.AsError(err).Code != model.ErrMP4TimeBase {
		t.Fatalf("want ErrMP4TimeBase, got %v", err)
	}
}
