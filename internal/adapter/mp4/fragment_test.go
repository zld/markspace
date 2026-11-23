package mp4

import (
	"bytes"
	"encoding/binary"
	"testing"

	"mediaqc/internal/adapter"
	"mediaqc/internal/model"
)

// box builds a minimal MP4 box: [size uint32][4cc][payload].
func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func fullBox(version byte, flags uint32) []byte {
	b := make([]byte, 4)
	b[0] = version
	b[1] = byte(flags >> 16)
	b[2] = byte(flags >> 8)
	b[3] = byte(flags)
	return b
}

// buildFragment assembles moof(traf(tfhd,tfdt,trun))+mdat using a buffer so
// sibling boxes never share backing arrays.
func buildFragment(trackID uint32, baseTime uint64, samples [][2]uint32, mdat []byte) []byte {
	tfhd := fullBox(0, 0)
	id := make([]byte, 4)
	binary.BigEndian.PutUint32(id, trackID)
	tfhd = append(tfhd, id...)

	tfdt := fullBox(1, 0)
	bt := make([]byte, 8)
	binary.BigEndian.PutUint64(bt, baseTime)
	tfdt = append(tfdt, bt...)

	trun := fullBox(0, 0x000300) // sample-duration + sample-size present
	sc := make([]byte, 4)
	binary.BigEndian.PutUint32(sc, uint32(len(samples)))
	trun = append(trun, sc...)
	for _, s := range samples {
		d := make([]byte, 8)
		binary.BigEndian.PutUint32(d, s[0])
		binary.BigEndian.PutUint32(d[4:], s[1])
		trun = append(trun, d...)
	}

	var traf bytes.Buffer
	traf.Write(box("tfhd", tfhd))
	traf.Write(box("tfdt", tfdt))
	traf.Write(box("trun", trun))
	var out bytes.Buffer
	out.Write(box("moof", box("traf", traf.Bytes())))
	out.Write(box("mdat", mdat))
	return out.Bytes()
}

func TestParseValidFragment(t *testing.T) {
	raw := buildFragment(7, 9000, [][2]uint32{{3000, 100}, {3000, 120}}, []byte("payload-bytes"))
	p := NewParser(adapter.DefaultLimits())
	frag, err := p.ParseBytes("mat-mp4-1", "v", 90000, 3, raw)
	if err != nil {
		t.Fatal(err)
	}
	if frag.TrackID != 7 || frag.BaseMediaTime != 9000 || frag.Sequence != 3 {
		t.Fatalf("fragment header wrong: %+v", frag)
	}
	if len(frag.Samples) != 2 || frag.Samples[1].Size != 120 {
		t.Fatalf("trun samples wrong: %+v", frag.Samples)
	}
	ev, err := frag.Event("mat-mp4-1", "v", model.TrackVideo)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Start.Ticks() != 9000 || ev.Duration.Ticks() != 6000 {
		t.Fatalf("event timing wrong: start=%d dur=%d", ev.Start.Ticks(), ev.Duration.Ticks())
	}
}

func TestTruncatedFragmentRejected(t *testing.T) {
	raw := buildFragment(1, 0, [][2]uint32{{100, 50}}, []byte("x"))
	p := NewParser(adapter.DefaultLimits())
	// Cut inside the moof so a declared box size runs past EOF.
	moofSize := int(raw[0])<<24 | int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
	if _, err := p.ParseBytes("mat-cut", "v", 90000, 1, raw[:moofSize-8]); err == nil {
		t.Fatal("truncated fragment must be rejected")
	}
	// Fragment without tfdt/trun.
	tfhdOnly := append(fullBox(0, 0), 0, 0, 0, 1)
	bare := box("moof", box("traf", box("tfhd", tfhdOnly)))
	_, err := p.ParseBytes("mat-bare", "v", 90000, 1, bare)
	if err == nil || !model.HasCode(err, model.CodeTruncatedFragment) {
		t.Fatalf("want truncated-fragment error, got %v", err)
	}
	if model.IsRecoverable(err) {
		t.Fatal("truncated fragment must be unrecoverable")
	}
}

func TestZeroTimescaleAndOversize(t *testing.T) {
	raw := buildFragment(1, 0, [][2]uint32{{100, 50}}, []byte("x"))
	p := NewParser(adapter.DefaultLimits())
	if _, err := p.ParseBytes("mat-zero", "v", 0, 1, raw); err == nil ||
		!model.HasCode(err, model.CodeZeroTimescale) {
		t.Fatalf("want zero-timescale error, got %v", err)
	}
	lim := adapter.DefaultLimits()
	lim.MaxBytes = 8
	if _, err := NewParser(lim).ParseBytes("mat-big", "v", 90000, 1, raw); err == nil {
		t.Fatal("oversized fragment must be rejected")
	}
}

func TestRegistryRegistration(t *testing.T) {
	got, err := adapter.Default.Lookup(adapter.KindMP4, "fmp4-local", "1.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Supports("tfdt") {
		t.Fatal("mp4 parser must advertise tfdt support")
	}
}
