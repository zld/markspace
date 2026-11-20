package index

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"mp4seek/internal/bmff"
	"mp4seek/internal/fixture"
)

func moofsOf(t *testing.T, r *bmff.Reader) []bmff.Box {
	t.Helper()
	var out []bmff.Box
	for _, b := range topBoxes(t, r) {
		if b.Type == "moof" {
			out = append(out, b)
		}
	}
	if len(out) == 0 {
		t.Fatal("no moof boxes")
	}
	return out
}

func TestFragmentedWrap(t *testing.T) {
	r := readerFromFixture(t, fixture.ProfileFragmentedWrap)
	moov := moovOf(t, r)
	trex, err := ParseTrex(r, moov, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !trex.Present || trex.Duration != 1000 {
		t.Fatalf("trex: %+v", trex)
	}
	idx, err := BuildFragmented(r, moofsOf(t, r), 1, trex)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Samples) != 12 {
		t.Fatalf("want 12 samples, got %d", len(idx.Samples))
	}
	if idx.WrapEvents != 1 {
		t.Fatalf("wrap_events = %d, want 1", idx.WrapEvents)
	}
	const two32 = 1 << 32
	if idx.MaxDTS <= two32-1 {
		t.Fatalf("max_dts %d not above 2^32-1", idx.MaxDTS)
	}
	if idx.MaxDTS != two32+4000+3000 {
		t.Fatalf("max_dts = %d", idx.MaxDTS)
	}
	// Fragment boundaries: sample 4 starts fragment 2 at exactly 2^32.
	if got := idx.Samples[4].DTS; got != two32 {
		t.Fatalf("sample 4 DTS = %d, want %d", got, two32)
	}
	if got := idx.Samples[8].DTS; got != two32+4000 {
		t.Fatalf("sample 8 DTS = %d", got)
	}
	if !idx.Samples[0].Key || idx.Samples[1].Key {
		t.Fatal("key flags wrong")
	}
	// Offsets must be readable.
	for _, s := range idx.Samples {
		if _, err := r.ReadSample(s.Offset, s.Size); err != nil {
			t.Fatalf("sample %d unreadable: %v", s.Index, err)
		}
	}
}

// mkMoof builds a minimal moof+mdat pair for regression tests.
func mkMoof(tfdt uint32, sizes []uint32, media []byte) []byte {
	box := func(typ string, payload []byte) []byte {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(len(payload)+8))
		return append(append(b[:], typ...), payload...)
	}
	full := func(typ string, flags uint32, payload []byte) []byte {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], flags)
		return box(typ, append(b[:], payload...))
	}
	u32 := func(v uint32) []byte {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], v)
		return b[:]
	}
	var tfhd []byte
	tfhd = append(tfhd, u32(1)...)    // track_ID
	tfhd = append(tfhd, u32(1000)...) // default duration
	var trun []byte
	trun = append(trun, u32(uint32(len(sizes)))...)
	dataOffPos := len(trun)
	trun = append(trun, u32(0)...)
	for _, s := range sizes {
		trun = append(trun, u32(s)...)
	}
	trafPayload := func(dataOff uint32) []byte {
		tr := append([]byte(nil), trun...)
		binary.BigEndian.PutUint32(tr[dataOffPos:], dataOff)
		var out []byte
		out = append(out, full("tfhd", 0x020008, tfhd)...)
		out = append(out, full("tfdt", 0, u32(tfdt))...)
		out = append(out, full("trun", 0x000201, tr)...) // data-offset + sample-size
		return box("traf", out)
	}
	probe := trafPayload(0)
	moofSize := 8 + len(full("mfhd", 0, u32(1))) + len(probe)
	var moof []byte
	moof = append(moof, full("mfhd", 0, u32(1))...)
	moof = append(moof, trafPayload(uint32(moofSize+8))...)
	return append(box("moof", moof), box("mdat", media)...)
}

func TestFragmentedRegressionRejected(t *testing.T) {
	// Two fragments whose tfdt steps backward by a small amount: not a
	// wrap, must be rejected.
	media := make([]byte, 4*8)
	var data []byte
	data = append(data, mkMoof(5000, []uint32{8, 8}, media[:16])...)
	data = append(data, mkMoof(4000, []uint32{8, 8}, media[16:])...)
	r := bmff.NewReader(bytes.NewReader(data), int64(len(data)))
	var moofs []bmff.Box
	if err := r.WalkTop(func(b bmff.Box) (bool, error) {
		if b.Type == "moof" {
			moofs = append(moofs, b)
		}
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := BuildFragmented(r, moofs, 1, TrexDefaults{Present: true, Duration: 1000})
	if !errors.Is(err, bmff.ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}
