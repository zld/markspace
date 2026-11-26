package scan

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"mediad/internal/model"
)

// box builds an ISOBMFF box: size|type|payload.
func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func fullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	hdr := make([]byte, 4)
	hdr[0] = version
	hdr[1] = byte(flags >> 16)
	hdr[2] = byte(flags >> 8)
	hdr[3] = byte(flags)
	return box(typ, cat(hdr, payload))
}

func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

// cat concatenates byte slices without the variadic foot-gun of append.
func cat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// fixedV0 boxes below are padded to the exact ISOBMFF v0 payload sizes that
// go-mp4's strict unmarshaller requires.
func mvhdBox() []byte {
	p := cat(u32(0), u32(0), u32(1000), u32(2000)) // ctime,mtime,ts,duration
	p = cat(p, make([]byte, 96-16))                // rate..next-track-id
	return fullBox("mvhd", 0, 0, p)
}

func tkhdBox(id uint32) []byte {
	p := cat(u32(0), u32(0), u32(id), u32(0), u32(0)) // 20 bytes
	p = cat(p, make([]byte, 80-20))
	return fullBox("tkhd", 0, 0x3, p)
}

func mdhdBox(timescale, duration uint32) []byte {
	p := cat(u32(0), u32(0), u32(timescale), u32(duration), make([]byte, 4))
	return fullBox("mdhd", 0, 0, p)
}

func trexBox(track uint32) []byte {
	return fullBox("trex", 0, 0, make([]byte, 20)) // fields defaulted to zero
}

// buildFragmentedMP4 emits a minimal fMP4: ftyp/moov + moof/mdat, with an
// emsg v1 box ahead of the fragment, timescale 1000, 4 samples of 500 ticks.
func buildFragmentedMP4() []byte {
	// ftyp
	ftyp := box("ftyp", cat([]byte("iso5"), u32(1), []byte("iso5dash")))
	// moov > mvhd(1000), trak > tkhd(id=1), mdia > mdhd(1000, dur 2000), hdlr(vide)
	hdlr := fullBox("hdlr", 0, 0, cat(u32(0), []byte("vide"), make([]byte, 12), []byte("v\x00")))
	stbl := box("stbl", fullBox("stsd", 0, 0, u32(0)))
	mdia := box("mdia", cat(mdhdBox(1000, 2000), hdlr, box("minf", stbl)))
	trak := box("trak", cat(tkhdBox(1), mdia))
	moov := box("moov", cat(mvhdBox(), trak, trexBox(1)))

	// emsg version 1 with its own timescale 90000
	emsgPayload := cat([]byte("urn:test\x00"), []byte("v1\x00"),
		u32(90000), // timescale
		u64(45000), // presentation time (0.5 s)
		u32(9000),  // duration
		u32(7),     // id
		[]byte("hello"))
	emsg := fullBox("emsg", 1, 0, emsgPayload)

	// moof: mfhd(seq=1), traf: tfhd(track 1, default-base-is-moof), tfdt(base=0),
	// trun with 4 samples, data offset present, duration+size flags.
	mfhd := fullBox("mfhd", 0, 0, u32(1))
	tfhd := fullBox("tfhd", 0, 0x020000, u32(1))
	tfdt := fullBox("tfdt", 0, 0, u32(0))
	var trunBody []byte
	trunBody = cat(trunBody, u32(4), u32(0)) // sample count, data-offset placeholder
	for i := 0; i < 4; i++ {
		trunBody = cat(trunBody, u32(500), u32(10)) // duration then size
	}
	trun := fullBox("trun", 0, 0x301, trunBody)
	moof := box("moof", cat(mfhd, box("traf", cat(tfhd, tfdt, trun))))

	// Patch trun data offset relative to moof start: mdat payload begins after
	// the moof and the 8-byte mdat header.
	mdat := box("mdat", make([]byte, 40))
	frag := cat(moof, mdat)
	trunOff := strings.Index(string(frag), "trun")
	binary.BigEndian.PutUint32(frag[trunOff+4+4+4:], uint32(len(moof)+8))

	return cat(ftyp, moov, emsg, frag)
}

func TestScanFragmentedMP4(t *testing.T) {
	data := buildFragmentedMP4()
	res, err := ScanMP4(bytes.NewReader(data), "clip.fmp4", "hash-1")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Fragmented {
		t.Fatal("expected fragmented result")
	}
	if len(res.Tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(res.Tracks))
	}
	tr := res.Tracks[0]
	if tr.TimeBase.Den != 1000 || tr.Kind != KindVideo {
		t.Fatalf("unexpected track: %+v", tr)
	}
	if len(res.Samples) != 4 {
		t.Fatalf("samples = %d, want 4", len(res.Samples))
	}
	for i, s := range res.Samples {
		if s.Tick != int64(i*500) {
			t.Fatalf("sample %d tick = %d", i, s.Tick)
		}
		if s.Length != 10 {
			t.Fatalf("sample %d length = %d", i, s.Length)
		}
		if i > 0 && s.Offset != res.Samples[i-1].Offset+10 {
			t.Fatalf("sample %d offset not contiguous: %d", i, s.Offset)
		}
	}
	if len(res.Events) != 1 || res.Events[0].Kind != EventEmsg {
		t.Fatalf("events = %+v", res.Events)
	}
	ev := res.Events[0]
	if ev.TimeBase.Den != 90000 || ev.Tick != 45000 || ev.ID != "emsg-7" {
		t.Fatalf("emsg mismatch: %+v", ev)
	}
	ns, err := ev.TimeBase.ToNanos(ev.Tick)
	if err != nil || ns != 500_000_000 {
		t.Fatalf("emsg nanos = %d, err=%v", ns, err)
	}
}

func TestScanRejectsZeroTimescale(t *testing.T) {
	moov := box("moov", box("trak", box("mdia", cat(tkhdBox(1), mdhdBox(0, 10)))))
	clip := cat(box("ftyp", cat([]byte("iso5"), u32(1))), moov)
	if _, err := ScanMP4(bytes.NewReader(clip), "bad.mp4", "h"); err == nil {
		t.Fatal("expected zero-timescale error")
	}
}

func TestScanParserVersionStable(t *testing.T) {
	if ParserVersion == "" {
		t.Fatal("parser version must be non-empty")
	}
	var _ model.AnomalyType = model.AnomalyUnparseable
}
