package cmaf

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	mp4 "github.com/abema/go-mp4"

	"replay/internal/model"
)

// --- byte-level fixture builders ---

func makeBox(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	copy(b[4:], typ)
	copy(b[8:], payload)
	return b
}

func fullBox(ver byte, flags uint32, payload []byte) []byte {
	h := []byte{ver, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return append(h, payload...)
}

func mfhdBox(seq uint32) []byte {
	p := make([]byte, 4)
	binary.BigEndian.PutUint32(p, seq)
	return makeBox("mfhd", fullBox(0, 0, p))
}

func tfdtBox(v uint64) []byte {
	p := make([]byte, 8)
	binary.BigEndian.PutUint64(p, v)
	return makeBox("tfdt", fullBox(1, 0, p))
}

func tfhdBox(trackID, defDur uint32) []byte {
	p := make([]byte, 8)
	binary.BigEndian.PutUint32(p, trackID)
	binary.BigEndian.PutUint32(p[4:], defDur)
	return makeBox("tfhd", fullBox(0, 0x000008, p))
}

func trunBox(durs ...uint32) []byte {
	p := make([]byte, 4+4*len(durs))
	binary.BigEndian.PutUint32(p, uint32(len(durs)))
	for i, d := range durs {
		binary.BigEndian.PutUint32(p[4+4*i:], d)
	}
	return makeBox("trun", fullBox(0, 0x000100, p))
}

func trafBox(trackID uint32, tfdt uint64, durs ...uint32) []byte {
	return makeBox("traf", concat(tfhdBox(trackID, 0), tfdtBox(tfdt), trunBox(durs...)))
}

func moofBox(seq uint32, trafs ...[]byte) []byte {
	body := mfhdBox(seq)
	for _, t := range trafs {
		body = append(body, t...)
	}
	return makeBox("moof", body)
}

func mdatBox(n int) []byte { return makeBox("mdat", make([]byte, n)) }

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// marshalPayload marshals a box payload with go-mp4 (headers are added by
// makeBox): mp4.Marshal emits only the payload.
func marshalPayload(t *testing.T, b mp4.IImmutableBox) []byte {
	t.Helper()
	var buf []byte
	w := &byteWriter{&buf}
	if _, err := mp4.Marshal(w, b, mp4.Context{}); err != nil {
		t.Fatalf("marshal %T: %v", b, err)
	}
	return buf
}

type byteWriter struct{ p *[]byte }

func (w *byteWriter) Write(b []byte) (int, error) {
	*w.p = append(*w.p, b...)
	return len(b), nil
}

func stsdBox(entries ...[]byte) []byte {
	p := make([]byte, 4)
	binary.BigEndian.PutUint32(p, uint32(len(entries)))
	return makeBox("stsd", append(fullBox(0, 0, p), concat(entries...)...))
}

func avc1Entry(w, h uint16, colr []byte) []byte {
	e := make([]byte, 70)
	binary.BigEndian.PutUint16(e[24:], w)
	binary.BigEndian.PutUint16(e[26:], h)
	return makeBox("avc1", append(e, colr...))
}

func colrBox(prim, trc, mtx uint16) []byte {
	p := make([]byte, 11)
	copy(p, "nclx")
	binary.BigEndian.PutUint16(p[4:], prim)
	binary.BigEndian.PutUint16(p[6:], trc)
	binary.BigEndian.PutUint16(p[8:], mtx)
	return makeBox("colr", p)
}

func mp4aEntry(ch uint16, rate uint32) []byte {
	e := make([]byte, 28)
	binary.BigEndian.PutUint16(e[16:], ch)
	binary.BigEndian.PutUint32(e[24:], rate<<16)
	return makeBox("mp4a", e)
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// buildInit writes a minimal init segment: video 90kHz bt709, audio 48k stereo.
func buildInit(t *testing.T) string {
	t.Helper()
	tkhd1 := makeBox("tkhd", marshalPayload(t, &mp4.Tkhd{TrackID: 1}))
	mdhd1 := makeBox("mdhd", marshalPayload(t, &mp4.Mdhd{Timescale: 90000}))
	hdlr1 := makeBox("hdlr", marshalPayload(t, &mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}}))
	stsd1 := stsdBox(avc1Entry(640, 360, colrBox(1, 1, 1)))
	trak1 := makeBox("trak", concat(tkhd1,
		makeBox("mdia", concat(mdhd1, hdlr1, makeBox("minf", makeBox("stbl", stsd1))))))
	tkhd2 := makeBox("tkhd", marshalPayload(t, &mp4.Tkhd{TrackID: 2}))
	mdhd2 := makeBox("mdhd", marshalPayload(t, &mp4.Mdhd{Timescale: 48000}))
	hdlr2 := makeBox("hdlr", marshalPayload(t, &mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}}))
	stsd2 := stsdBox(mp4aEntry(2, 48000))
	trak2 := makeBox("trak", concat(tkhd2,
		makeBox("mdia", concat(mdhd2, hdlr2, makeBox("minf", makeBox("stbl", stsd2))))))
	moov := makeBox("moov", concat(trak1, trak2))
	return writeFile(t, "init.mp4", concat(makeBox("ftyp", []byte("cmfc")), moov))
}

func testInit() *Init {
	return &Init{Tracks: []TrackInfo{
		{TrackID: 1, Type: TrackVideo, Timescale: 90000},
		{TrackID: 2, Type: TrackAudio, Timescale: 48000},
	}}
}

// --- tests ---

func TestParseInit(t *testing.T) {
	p := NewParser(nil)
	init, err := p.ParseInit(buildInit(t))
	if err != nil {
		t.Fatal(err)
	}
	v := init.Video()
	if v == nil || v.Timescale != 90000 || v.Width != 640 || v.Height != 360 {
		t.Fatalf("bad video track: %+v", v)
	}
	if v.Color == nil || v.Color.Primaries != "bt709" || v.Color.Matrix != "bt709" {
		t.Fatalf("bad color: %+v", v.Color)
	}
	a := init.Audio()
	if a == nil || a.Audio == nil || a.Audio.Channels != 2 || a.Audio.SampleRate != 48000 {
		t.Fatalf("bad audio track: %+v", a)
	}
}

func TestParseFragmentOK(t *testing.T) {
	frag := concat(
		moofBox(3, trafBox(1, 180000, 512, 512), trafBox(2, 96000, 1024)),
		mdatBox(100),
	)
	path := writeFile(t, "seg_3.m4s", frag)
	p := NewParser(nil)
	f, err := p.ParseFragment(path, testInit())
	if err != nil {
		t.Fatal(err)
	}
	if f == nil || f.Seq != 3 || f.Tfdt != 180000 || f.VideoDur != 1024 {
		t.Fatalf("bad fragment: %+v", f)
	}
	if !f.HasAudio || f.AudioTfdt != 96000 || f.AudioDur != 1024 {
		t.Fatalf("bad audio traf: %+v", f)
	}
	if f.Damaged {
		t.Fatal("fragment must not be marked damaged")
	}
}

func TestParseFragmentTruncatedMdat(t *testing.T) {
	good := concat(moofBox(4, trafBox(1, 0, 100)), mdatBox(200))
	path := writeFile(t, "seg_4.m4s", good[:len(good)-50]) // cut inside mdat
	rep := &model.Report{}
	p := NewParser(rep)
	f, err := p.ParseFragment(path, testInit())
	if err != nil {
		t.Fatal(err)
	}
	if f == nil || !f.Damaged {
		t.Fatalf("expected damaged fragment, got %+v", f)
	}
	if rep.Count(model.CodeTruncatedBox) != 1 {
		t.Fatalf("expected 1 truncation issue, got %d", rep.Count(model.CodeTruncatedBox))
	}
	iss := rep.Issues[0]
	if iss.Offset != int64(len(good)-208) { // mdat header offset
		t.Fatalf("issue offset = %d, want %d", iss.Offset, len(good)-208)
	}
}

func TestParseFragmentTruncatedMoof(t *testing.T) {
	good := concat(moofBox(5, trafBox(1, 0, 100)), mdatBox(10))
	path := writeFile(t, "seg_5.m4s", good[:20]) // cut inside moof
	rep := &model.Report{}
	p := NewParser(rep)
	f, err := p.ParseFragment(path, testInit())
	if err != nil {
		t.Fatal(err)
	}
	if f != nil {
		t.Fatalf("expected nil fragment for cut moof, got %+v", f)
	}
	if rep.Count(model.CodeTruncatedBox) != 1 {
		t.Fatal("expected truncation issue")
	}
}

func TestParseFragmentDefaultDuration(t *testing.T) {
	traf := makeBox("traf", concat(tfhdBox(1, 512), tfdtBox(0),
		makeBox("trun", fullBox(0, 0, []byte{0, 0, 0, 3})))) // 3 samples, no durations
	path := writeFile(t, "seg_1.m4s", concat(moofBox(1, traf), mdatBox(10)))
	p := NewParser(nil)
	f, err := p.ParseFragment(path, testInit())
	if err != nil {
		t.Fatal(err)
	}
	if f.VideoDur != 1536 {
		t.Fatalf("VideoDur = %d, want 1536 (3 x default 512)", f.VideoDur)
	}
}
