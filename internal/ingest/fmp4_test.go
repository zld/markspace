package ingest

import (
	"audionorm/internal/media"
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

// box builds a length-prefixed MP4 box from type and payload.
func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(8+len(payload)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

// fullBox is a FullBox payload (version+flags then inner bytes).
func fullBox(version byte, flags uint32, inner []byte) []byte {
	out := make([]byte, 4+len(inner))
	out[0] = version
	out[1] = byte(flags >> 16)
	out[2] = byte(flags >> 8)
	out[3] = byte(flags)
	copy(out[4:], inner)
	return out
}

// buildFMP4 builds a minimal audio fMP4: moov(mdhd timescale) then two
// moof/mdat fragments, each with two AAC samples. If truncTrun is true,
// the second trun declares 2 samples but carries only one entry.
func buildFMP4(t *testing.T, timescale uint32, truncTrun bool) []byte {
	t.Helper()
	// mdhd v0: creation/mod u32×2, timescale, duration, language u16, pre-defined u16.
	mdhdPayload := make([]byte, 4+20)
	binary.BigEndian.PutUint32(mdhdPayload[4:8], 0)
	binary.BigEndian.PutUint32(mdhdPayload[8:12], 0)
	binary.BigEndian.PutUint32(mdhdPayload[12:16], timescale)
	binary.BigEndian.PutUint32(mdhdPayload[16:20], 1024)
	moov := box("moov",
		box("trak", box("mdia",
			box("mdhd", mdhdPayload))))

	var out bytes.Buffer
	out.Write(moov)
	writeFragment := func(seq, base uint64, count uint32, trunc bool) {
		// mfhd
		mfhd := box("mfhd", fullBox(0, 0, u32(uint32(seq))))
		tfdt := box("tfdt", fullBox(1, 0, u64(base)))
		var entries []byte
		real := count
		if trunc {
			real = 1
		}
		for i := uint32(0); i < real; i++ {
			entries = append(entries, u32(1024)...) // duration
			entries = append(entries, u32(4)...)    // size
		}
		// flags(24-bit): data-offset(0x1) | duration(0x10000) | size(0x20000)
		trunInner := append(u32(count), u32(0)...) // SampleCount, DataOffset placeholder
		trunInner = append(trunInner, entries...)
		trun := box("trun", fullBox(0, 0x301, trunInner))
		traf := box("traf", append(append(mfhd, tfdt...), trun...))
		moof := box("moof", traf)
		// Patch DataOffset = header sizes from moof start to mdat data.
		dataOffset := len(moof) + 8
		// trun box layout: 8 header + 4 fullbox + 4 SampleCount, then DataOffset.
		trunType := bytes.Index(moof, []byte("trun")) // points at type, 4 bytes into box
		dataOffsetField := trunType + 4 + 4 + 4       // type(4) + fullbox(4) + count(4)
		binary.BigEndian.PutUint32(moof[dataOffsetField:dataOffsetField+4], uint32(dataOffset))
		mdat := box("mdat", bytes.Repeat([]byte{0xab}, int(count)*4))
		out.Write(moof)
		out.Write(mdat)
	}
	writeFragment(1, 0, 2, false)
	writeFragment(2, 2048, 2, truncTrun)
	return out.Bytes()
}

func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

func TestFMP4HappyPath(t *testing.T) {
	data := buildFMP4(t, 48000, false)
	frags, ts, errs, err := ParseFMP4(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if ts != 48000 || errs.Len() != 0 {
		t.Fatalf("timescale=%d errs=%v", ts, errs.All())
	}
	if len(frags) != 4 {
		t.Fatalf("fragments = %d, want 4", len(frags))
	}
	// First fragment: base 0, 1024 movie units -> 1024 timeline samples.
	in := NewFMP4Ingester(ts, 2)
	for _, f := range frags {
		payload := data[f.Offset : f.Offset+int64(f.Size)]
		in.AddFragment(f, payload)
	}
	res := in.Result()
	if len(res.Packets) != 4 {
		t.Fatalf("packets = %d, want 4", len(res.Packets))
	}
	if p0 := res.Packets[0]; p0.When.Start != 0 || p0.When.End != 1024 {
		t.Fatalf("first interval = [%d,%d)", p0.When.Start, p0.When.End)
	}
	if last := res.Packets[3]; last.When.End != 4096 {
		t.Fatalf("last end = %d, want 4096", last.When.End)
	}
}

func TestFMP4TruncatedIndexHolds(t *testing.T) {
	data := buildFMP4(t, 48000, true)
	frags, _, errs, err := ParseFMP4(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !errs.Has(media.ErrIndexIncomplete) {
		t.Fatalf("truncated trun not reported: %v", errs.All())
	}
	// Only the three fully-indexed samples may be returned.
	if len(frags) != 3 {
		t.Fatalf("fragments = %d, want 3 (no guessing)", len(frags))
	}
	rec := errs.All()[0]
	if rec.Locatable {
		t.Fatal("truncated index must be pending/HOLD, not locatable")
	}
}

func TestFMP4TruncatedBoxStream(t *testing.T) {
	data := buildFMP4(t, 48000, false)
	// Cut the stream mid-way through the second moof header region.
	cut := len(data) - 30
	_, _, errs, err := ParseFMP4(bytes.NewReader(data[:cut]))
	if err != nil && err != io.EOF {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if !errs.Has(media.ErrIndexIncomplete) {
		t.Fatal("cut stream must yield INDEX_INCOMPLETE, not silent success")
	}
}
