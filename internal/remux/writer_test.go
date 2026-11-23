package remux

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediatool/internal/mp4"
	"mediatool/internal/timeline"
)

func TestSafePathAllowsContained(t *testing.T) {
	root := t.TempDir()
	got, err := SafePath(root, "video/chunk-1.m4s")
	if err != nil {
		t.Fatalf("contained path rejected: %v", err)
	}
	if !strings.HasPrefix(got, root) {
		t.Fatalf("path %q not under root %q", got, root)
	}
}

func TestSafePathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"../escape.m4s", "../../etc/passwd", "a/../../b"} {
		if _, err := SafePath(root, bad); err == nil {
			t.Fatalf("path %q must be rejected", bad)
		} else if timeline.AsMediaError(err).Class != timeline.ClassUnsafePath {
			t.Fatalf("path %q wrong error class: %v", bad, err)
		}
	}
	if _, err := SafePath(root, "/etc/passwd"); err == nil {
		t.Fatal("absolute path must be rejected")
	}
}

func payloadFor(seed byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i%7)
	}
	return b
}

func TestRemuxLosslessAndDeterministic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "out")
	track := mp4.TrackInfo{TrackID: 1, Timescale: 90000, MediaType: "video", CodecKey: "avc1"}
	runOnce := func() (*Manifest, map[int][32]byte) {
		w, err := NewWriter(root)
		if err != nil {
			t.Fatal(err)
		}
		// Fragments arrive out of order; OrderKey encodes corrected order.
		specs := []struct {
			order int
			dts   uint64
			seed  byte
			n     int
		}{
			{2, 180000, 0x10, 40},
			{0, 0, 0x20, 32},
			{1, 90000, 0x30, 36},
		}
		hashes := make(map[int][32]byte)
		for _, s := range specs {
			payload := payloadFor(s.seed, s.n)
			meta := SpoolMeta{
				Slot: "video-main", OrderKey: s.order, InputOrder: s.order,
				Seq: s.order, TrackID: 1, CorrectedDTS: s.dts, BaseMediaDecode: s.dts,
				Timescale:       90000,
				SampleDurations: []uint32{3003},
				SampleSizes:     []uint32{uint32(s.n)},
			}
			if err := w.Spool(meta, bytes.NewReader(payload), len(payload)); err != nil {
				t.Fatal(err)
			}
			hashes[s.order] = sha256.Sum256(payload)
		}
		manifest, err := w.Finalize([]FinalizePlan{{Slot: "video-main", Track: track, Metas: w.Metas()}})
		if err != nil {
			t.Fatal(err)
		}
		return manifest, hashes
	}

	first, hashes := runOnce()
	if len(first.Slots) != 1 || len(first.Slots[0].Fragments) != 3 {
		t.Fatalf("manifest shape wrong: %+v", first)
	}
	// Output ordering follows corrected DTS, not arrival order.
	gotDTS := []uint64{
		first.Slots[0].Fragments[0].CorrectedDTS,
		first.Slots[0].Fragments[1].CorrectedDTS,
		first.Slots[0].Fragments[2].CorrectedDTS,
	}
	if gotDTS[0] != 0 || gotDTS[1] != 90000 || gotDTS[2] != 180000 {
		t.Fatalf("fragments not ordered by DTS: %v", gotDTS)
	}
	// Contiguous output sequence numbers.
	for i, f := range first.Slots[0].Fragments {
		if f.OutputSeq != i {
			t.Fatalf("output seq gap: %+v", f)
		}
	}
	// Verify payload bytes survive by re-parsing output chunks.
	for _, f := range first.Slots[0].Fragments {
		raw, err := os.ReadFile(filepath.Join(root, f.File))
		if err != nil {
			t.Fatal(err)
		}
		frag, err := mp4.ParseFragment(bytes.NewReader(raw), 0)
		if err != nil || frag.Rejected {
			t.Fatalf("output chunk %d invalid: %v %+v", f.OutputSeq, err, frag.Errors)
		}
		want := hashes[f.OutputSeq]
		if got := sha256.Sum256(frag.MdatPayload); got != want {
			t.Fatalf("chunk %d payload hash changed", f.OutputSeq)
		}
		// The hash recorded in the manifest must match as well.
		if f.PayloadHashHex != hex.EncodeToString(want[:]) {
			t.Fatalf("chunk %d manifest hash mismatch", f.OutputSeq)
		}
	}
	// A second run on a fresh root with identical input must hash equal.
	root2 := filepath.Join(t.TempDir(), "out2")
	w2, err := NewWriter(root2)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct {
		order int
		dts   uint64
		seed  byte
		n     int
	}{
		{2, 180000, 0x10, 40}, {0, 0, 0x20, 32}, {1, 90000, 0x30, 36},
	} {
		payload := payloadFor(s.seed, s.n)
		meta := SpoolMeta{
			Slot: "video-main", OrderKey: s.order, InputOrder: s.order,
			Seq: s.order, TrackID: 1, CorrectedDTS: s.dts, BaseMediaDecode: s.dts,
			Timescale: 90000, SampleDurations: []uint32{3003},
			SampleSizes: []uint32{uint32(s.n)},
		}
		if err := w2.Spool(meta, bytes.NewReader(payload), len(payload)); err != nil {
			t.Fatal(err)
		}
	}
	second, err := w2.Finalize([]FinalizePlan{{Slot: "video-main", Track: track, Metas: w2.Metas()}})
	if err != nil {
		t.Fatal(err)
	}
	if first.SummaryHash != second.SummaryHash || first.SummaryHash == "" {
		t.Fatalf("summary hash not reproducible: %q vs %q", first.SummaryHash, second.SummaryHash)
	}
	// Spool directory must be cleaned up after finalization.
	if _, err := os.Stat(filepath.Join(root, "spool")); !os.IsNotExist(err) {
		t.Fatal("spool directory should be removed")
	}
}

func TestRemuxLoadManifest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "out")
	w, err := NewWriter(root)
	if err != nil {
		t.Fatal(err)
	}
	payload := payloadFor(1, 16)
	meta := SpoolMeta{
		Slot: "a", OrderKey: 0, InputOrder: 0, Seq: 0, TrackID: 5,
		CorrectedDTS: 0, Timescale: 48000,
		SampleDurations: []uint32{1024}, SampleSizes: []uint32{16},
	}
	if err := w.Spool(meta, bytes.NewReader(payload), len(payload)); err != nil {
		t.Fatal(err)
	}
	track := mp4.TrackInfo{TrackID: 5, Timescale: 48000, MediaType: "audio", CodecKey: "mp4a"}
	m, err := w.Finalize([]FinalizePlan{{Slot: "a", Track: track, Metas: w.Metas()}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SummaryHash != m.SummaryHash || len(loaded.Slots) != 1 {
		t.Fatalf("loaded manifest mismatch: %+v vs %+v", loaded, m)
	}
}

func TestSpoolTruncatedPayloadRejected(t *testing.T) {
	root := filepath.Join(t.TempDir(), "out")
	w, err := NewWriter(root)
	if err != nil {
		t.Fatal(err)
	}
	meta := SpoolMeta{Slot: "x", OrderKey: 0, TrackID: 1, SampleSizes: []uint32{10}}
	err = w.Spool(meta, bytes.NewReader([]byte("short")), 10)
	if me := timeline.AsMediaError(err); me == nil || me.Class != timeline.ClassBoxTruncated {
		t.Fatalf("want truncated spool error, got %v", err)
	}
}

func TestSpoolSliceRangeRejected(t *testing.T) {
	root := filepath.Join(t.TempDir(), "out")
	w, err := NewWriter(root)
	if err != nil {
		t.Fatal(err)
	}
	payload := payloadFor(3, 16)
	// End beyond the spooled length must be rejected (overflow guard).
	meta := SpoolMeta{
		Slot: "x", OrderKey: 0, TrackID: 1,
		SampleSizes: []uint32{16}, PayloadStart: 0, PayloadEnd: 999,
	}
	err = w.Spool(meta, bytes.NewReader(payload), len(payload))
	if me := timeline.AsMediaError(err); me == nil || me.Class != timeline.ClassOverflow {
		t.Fatalf("want overflow for out-of-range slice, got %v", err)
	}
	// Start greater than end must also be rejected.
	meta2 := SpoolMeta{
		Slot: "x", OrderKey: 1, TrackID: 1,
		SampleSizes: []uint32{16}, PayloadStart: 10, PayloadEnd: 4,
	}
	err = w.Spool(meta2, bytes.NewReader(payload), len(payload))
	if me := timeline.AsMediaError(err); me == nil || me.Class != timeline.ClassOverflow {
		t.Fatalf("want overflow for inverted slice, got %v", err)
	}
}

func TestSpoolSubSliceRemuxedLosslessly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "out")
	w, err := NewWriter(root)
	if err != nil {
		t.Fatal(err)
	}
	// A shared spool holding 16 bytes; the slot owns only bytes [4:12].
	full := payloadFor(5, 16)
	meta := SpoolMeta{
		Slot: "sub", OrderKey: 0, InputOrder: 0, Seq: 0, TrackID: 8,
		CorrectedDTS: 0, Timescale: 48000,
		SampleDurations: []uint32{1024}, SampleSizes: []uint32{8},
		PayloadStart: 4, PayloadEnd: 12,
	}
	if err := w.Spool(meta, bytes.NewReader(full), len(full)); err != nil {
		t.Fatal(err)
	}
	track := mp4.TrackInfo{TrackID: 8, Timescale: 48000, MediaType: "audio", CodecKey: "mp4a"}
	m, err := w.Finalize([]FinalizePlan{{Slot: "sub", Track: track, Metas: w.Metas()}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, m.Slots[0].Fragments[0].File))
	if err != nil {
		t.Fatal(err)
	}
	frag, err := mp4.ParseFragment(bytes.NewReader(raw), 0)
	if err != nil || frag.Rejected {
		t.Fatalf("sub-slice chunk invalid: %v", err)
	}
	if !bytes.Equal(frag.MdatPayload, full[4:12]) {
		t.Fatal("sub-slice payload not remuxed byte-identically")
	}
}
