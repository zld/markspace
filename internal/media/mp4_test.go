package media

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// buildRawMP4 writes a minimal, structurally valid MP4 whose single video
// track stores uncompressed 4x4 luma planes in mdat.
func buildRawMP4(t *testing.T, frame []byte, frames int) []byte {
	t.Helper()
	// ftyp
	ftyp := box("ftyp", []byte("isom\x00\x00\x02\x00isomiso2mp41"))
	// moov > mvhd, trak { tkhd, mdia { mdhd, hdlr, minf { stbl { stsd, stts, stsc, stsz, stco } } } }
	plane := 16
	sampleSizes := make([]byte, 0)
	for i := 0; i < frames; i++ {
		sampleSizes = binary.BigEndian.AppendUint32(sampleSizes, uint32(plane))
	}
	stsz := box("stsz", concat(fullbox(0, 0, 0), u32(uint32(plane)), u32(uint32(frames))))
	_ = sampleSizes
	stts := box("stts", concat(fullbox(0, 0, 0), u32(1), u32(uint32(frames)), u32(90000/30)))
	stsc := box("stsc", concat(fullbox(0, 0, 0), u32(1), u32(1), u32(uint32(frames)), u32(1)))
	stsd := box("stsd", concat(fullbox(0, 0, 0), u32(1), rawSampleEntry(4, 4)))
	mdhd := box("mdhd", concat(fullbox(0, 0, 0), u32(0), u32(0), u32(90000), u32(uint32(frames*(90000/30))), []byte{0x55, 0xc4, 0, 0}))
	hdlr := box("hdlr", concat(fullbox(0, 0, 0), u32(0), []byte("vide"), make([]byte, 12), []byte("VideoHandler\x00")))
	tkhdPayload := concat(
		fullbox(0, 0, 7),
		u32(0), u32(0), // creation, modification
		u32(1), u32(0), // track id, reserved
		u32(0),         // duration
		u32(0), u32(0), // reserved[2]
		u32(0), u32(0), // layer+alternate group, volume+reserved
		u32(0x00010000), u32(0), u32(0),
		u32(0), u32(0x00010000), u32(0),
		u32(0), u32(0), u32(0x40000000),
		u32(4<<16), u32(4<<16), // width, height
	)
	tkhd := box("tkhd", tkhdPayload)
	stbl := box("stbl", concat(stsd, stts, stsc, stsz, box("stco", concat(fullbox(0, 0, 0), u32(1), u32(0)))))
	minf := box("minf", stbl)
	mdia := box("mdia", concat(mdhd, hdlr, minf))
	trak := box("trak", concat(tkhd, mdia))
	mvhdPayload := concat(
		fullbox(0, 0, 0),
		u32(0), u32(0), // creation, modification
		u32(1000), u32(0), // timescale, duration
		u32(0x00010000), // rate
		u32(0x0100),     // volume (int16)
		u32(0),          // reserved
		u32(0), u32(0),  // reserved[2]
		u32(0x00010000), u32(0), u32(0),
		u32(0), u32(0x00010000), u32(0),
		u32(0), u32(0), u32(0x40000000),
		u32(0), u32(0), u32(0), u32(0), u32(0), u32(0), // pre_defined[6]
		u32(2), // next track id
	)
	mvhd := box("mvhd", mvhdPayload)
	moov := box("moov", concat(mvhd, trak))
	// Patch stco to point at mdat payload; compute after layout.
	mdatHeader := box("mdat", []byte{})
	payloadLen := plane * frames
	mdatSize := 8 + payloadLen
	_ = mdatHeader
	prefixLen := len(ftyp) + len(moov)
	payload := make([]byte, payloadLen)
	for i := 0; i < frames; i++ {
		copy(payload[i*plane:], bytes.Repeat([]byte{byte(40 + i)}, plane))
	}
	mdat := box("mdat", payload)
	full := concat(ftyp, moov, mdat)
	stcoOff := bytes.Index(full, []byte("stco"))
	// stcoOff points at the 4-byte type; the first chunk offset sits at
	// boxStart+16 = stcoOff+12 (type + 4, header 8, version/flags 4, count 4).
	binary.BigEndian.PutUint32(full[stcoOff+4+8:], uint32(prefixLen+8))
	_ = mdatSize
	_ = frame
	return full
}

func box(typ string, payload []byte) []byte {
	out := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
	copy(out[4:], typ)
	return append(out, payload...)
}
func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func fullbox(ver byte, flags uint32, _ int) []byte {
	return []byte{ver, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}
func concat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
func rawSampleEntry(w, h uint16) []byte {
	p := make([]byte, 78)
	binary.BigEndian.PutUint32(p[0:], uint32(len(p)))
	copy(p[4:], "raw ")
	// SampleEntry: header(8) + reserved(6) + data_reference_index(2).
	binary.BigEndian.PutUint16(p[8+6:], 1)
	// VisualSampleEntry: pre_defined(2) reserved(2) pre_defined2(12)
	// then width(2) height(2) at offset 8+8+16.
	binary.BigEndian.PutUint16(p[8+8+16:], w)
	binary.BigEndian.PutUint16(p[8+8+18:], h)
	return p
}

func TestProbeMP4TimingAndTruncation(t *testing.T) {
	data := buildRawMP4(t, nil, 3)
	path := filepath.Join(t.TempDir(), "ok.mp4")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := ProbeFile(path, ProbeOptions{ExtractLuma: true})
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	v := p.Video()
	if v == nil || v.Timescale != 90000 || v.Width != 4 || v.Height != 4 {
		t.Fatalf("video track = %+v", v)
	}
	if len(v.SampleSizes) != 3 || len(v.SampleOffsets) != 3 {
		t.Fatalf("samples = %d offsets = %d", len(v.SampleSizes), len(v.SampleOffsets))
	}
	ns, err := v.DurationNanos()
	if err != nil || ns != 100_000_000 {
		t.Fatalf("duration = %d, %v", ns, err)
	}
	if len(p.Luma) != 3 || p.Luma[0][0] != 40 {
		t.Fatalf("luma frames missing")
	}
	// Truncate mid-mdat: the probe must report an offset, not silently pass.
	trunc := filepath.Join(t.TempDir(), "trunc.mp4")
	if err := os.WriteFile(trunc, data[:len(data)-5], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeFile(trunc, ProbeOptions{}); err == nil {
		t.Fatal("truncated mp4 must fail")
	} else if !bytes.Contains([]byte(err.Error()), []byte("trunc.mp4")) {
		t.Fatalf("error lacks path: %v", err)
	}
}
