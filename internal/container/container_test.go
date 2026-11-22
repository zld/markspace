package container

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediamerge/internal/clock"
)

// --- minimal MP4 builder for fixtures ---

func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:], typ)
	copy(b[8:], payload)
	return b
}

func fullbox(typ string, ver byte, payload []byte) []byte {
	return box(typ, append([]byte{ver, 0, 0, 0}, payload...))
}

func u32(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.BigEndian.PutUint32(b[4*i:], x)
	}
	return b
}

func tkhd(trackID uint32) []byte {
	p := append(u32(0, 0, trackID, 0, 0), make([]byte, 8)...) // times, id, reserved, duration, reserved
	p = append(p, make([]byte, 8+36+8)...)                    // layer/group/volume, matrix, width/height
	return fullbox("tkhd", 0, p)
}

func mdhd(timescale, duration uint32) []byte {
	return fullbox("mdhd", 0, append(u32(0, 0, timescale, duration), make([]byte, 4)...))
}

func hdlr(handler string) []byte {
	p := append(u32(0), []byte(handler)...)
	p = append(p, make([]byte, 12)...)
	return fullbox("hdlr", 0, append(p, 0))
}

type trackSpec struct {
	id        uint32
	timescale uint32
	handler   string
	deltas    []uint32 // stts single entry: count=len, delta=deltas[0]
	sizes     []uint32
	chunks    []uint32 // stco offsets, patched after layout
}

func buildMP4(t *testing.T, specs []trackSpec, mdatSize int) ([]byte, []uint32) {
	t.Helper()
	ftyp := box("ftyp", append([]byte("isom"), u32(0)...))
	var traks [][]byte
	// First pass: build with placeholder offsets to learn moov size.
	build := func(offsets [][]uint32) []byte {
		traks = traks[:0]
		for i, s := range specs {
			stts := fullbox("stts", 0, append(u32(1), u32(uint32(len(s.sizes)), s.deltas[0])...))
			stsc := fullbox("stsc", 0, append(u32(1), u32(1, uint32(len(s.sizes)), 1)...))
			stsz := fullbox("stsz", 0, append(u32(0, uint32(len(s.sizes))), u32(s.sizes...)...))
			stco := fullbox("stco", 0, append(u32(uint32(len(offsets[i]))), u32(offsets[i]...)...))
			stbl := box("stbl", concat(stts, stsc, stsz, stco))
			minf := box("minf", stbl)
			mdia := box("mdia", concat(mdhd(s.timescale, uint32(len(s.sizes))*s.deltas[0]), hdlr(s.handler), minf))
			traks = append(traks, box("trak", concat(tkhd(s.id), mdia)))
		}
		return box("moov", concat(traks...))
	}
	placeholder := make([][]uint32, len(specs))
	for i := range specs {
		placeholder[i] = []uint32{0}
	}
	moov := build(placeholder)
	mdatOff := len(ftyp) + len(moov)
	var real [][]uint32
	for range specs {
		real = append(real, []uint32{uint32(mdatOff + 8)})
	}
	moov = build(real)
	mdat := box("mdat", make([]byte, mdatSize))
	return concat(ftyp, moov, mdat), []uint32{uint32(mdatOff + 8)}
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.mp4")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func twoTrackSpec() []trackSpec {
	return []trackSpec{
		{id: 1, timescale: 30000, handler: "vide", deltas: []uint32{1001},
			sizes: []uint32{100, 100, 100}},
		{id: 2, timescale: 48000, handler: "soun", deltas: []uint32{1024},
			sizes: []uint32{50, 50, 50}},
	}
}

func TestParseValidFile(t *testing.T) {
	data, _ := buildMP4(t, twoTrackSpec(), 4096)
	fi, err := ParseFile(writeTemp(t, data), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(fi.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(fi.Tracks))
	}
	v, a := fi.Tracks[0], fi.Tracks[1]
	if v.TrackID != 1 || v.Timescale != 30000 || v.Kind != "video" {
		t.Fatalf("video track: %+v", v)
	}
	if a.TrackID != 2 || a.Timescale != 48000 || a.Kind != "audio" {
		t.Fatalf("audio track: %+v", a)
	}
	st, err := ExpandSamples(&v, fi.Size, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Samples) != 3 || st.TotalBytes != 300 {
		t.Fatalf("samples: %+v", st)
	}
	want := clock.Must(1001, 30000)
	if st.Samples[1].Timestamp.Cmp(want) != 0 {
		t.Fatalf("ts[1] = %v, want %v", st.Samples[1].Timestamp, want)
	}
	if st.Duration.Cmp(clock.Must(3003, 30000)) != 0 {
		t.Fatalf("duration = %v", st.Duration)
	}
}

func TestTruncatedBoxHeader(t *testing.T) {
	data, _ := buildMP4(t, twoTrackSpec(), 4096)
	trunc := data[:len(data)-2000] // cut inside mdat payload
	_, err := ParseFile(writeTemp(t, trunc), DefaultLimits())
	var be *BoxError
	if !errors.As(err, &be) {
		t.Fatalf("want BoxError, got %v", err)
	}
	if be.Offset == 0 {
		t.Fatal("offset not reported")
	}
}

func TestBoxSizeBeyondEOF(t *testing.T) {
	data, _ := buildMP4(t, twoTrackSpec(), 4096)
	// Corrupt the moov size to run past EOF: moov starts at len(ftyp).
	moovOff := 8 + 8 + 4 // ftyp header + payload("isom"+version)
	binary.BigEndian.PutUint32(data[moovOff:], uint32(len(data)*4))
	_, err := ParseFile(writeTemp(t, data), DefaultLimits())
	var be *BoxError
	if !errors.As(err, &be) {
		t.Fatalf("want BoxError, got %v", err)
	}
	if be.BoxType != "moov" {
		t.Fatalf("box type = %q", be.BoxType)
	}
}

func TestSampleOffsetBeyondFile(t *testing.T) {
	data, _ := buildMP4(t, twoTrackSpec(), 512) // mdat too small for 450 bytes? 450<512 ok
	fi, err := ParseFile(writeTemp(t, data), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt: push video chunk offset past EOF.
	fi.Tracks[0].ChunkOffsets[0] = uint64(fi.Size) + 100
	_, err = ExpandSamples(&fi.Tracks[0], fi.Size, DefaultLimits())
	var be *BoxError
	if !errors.As(err, &be) {
		t.Fatalf("want BoxError, got %v", err)
	}
	if be.TrackID != 1 || be.BoxType != "stco" {
		t.Fatalf("error = %v", be)
	}
}

func TestSampleCountLimit(t *testing.T) {
	data, _ := buildMP4(t, twoTrackSpec(), 4096)
	fi, err := ParseFile(writeTemp(t, data), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	lim := DefaultLimits()
	lim.MaxSamples = 2
	if _, err := ExpandSamples(&fi.Tracks[0], fi.Size, lim); err == nil {
		t.Fatal("want sample count limit error")
	}
}
