package mp4

import (
	"os"
	"path/filepath"
	"testing"

	gomp4 "github.com/abema/go-mp4"

	"mediascan/internal/clock"
	"mediascan/internal/fixture"
	"mediascan/internal/model"
)

func writeTemp(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func noErrorFaults(t *testing.T, res *Result) {
	t.Helper()
	for _, f := range res.Faults {
		if f.Severity == model.SeverityError {
			t.Fatalf("unexpected error fault: %+v", f)
		}
	}
}

func TestInspectProgressiveMatchesGroundTruth(t *testing.T) {
	spec := fixture.ProgressiveSpec{Timescale: 90000, Seed: 42}
	for i := 0; i < 60; i++ {
		spec.Samples = append(spec.Samples, fixture.SampleSpec{
			Delta: 3000, Size: 400 + (i%7)*31, Sync: i%20 == 0,
		})
	}
	built, err := fixture.BuildProgressive(spec)
	if err != nil {
		t.Fatal(err)
	}
	p := writeTemp(t, "a.mp4", built.Bytes)
	res, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != "progressive" || res.Timescale != 90000 {
		t.Fatalf("kind=%s ts=%d", res.Kind, res.Timescale)
	}
	noErrorFaults(t, res)
	if len(res.Tracks) != 1 || len(res.Tracks[0].Samples) != 60 {
		t.Fatalf("tracks=%d samples=%d", len(res.Tracks), sampleCount(res))
	}
	tr := res.Tracks[0]
	if len(tr.SyncIndices) != 3 {
		t.Fatalf("sync indices=%v want 3", tr.SyncIndices)
	}
	for i, gs := range built.Samples {
		got := tr.Samples[i]
		if got.Offset != gs.Offset || got.Size != uint32(gs.Size) || got.Delta != gs.Delta {
			t.Fatalf("sample %d mismatch got off=%d size=%d delta=%d want off=%d size=%d",
				i, got.Offset, got.Size, got.Delta, gs.Offset, gs.Size)
		}
		if got.TimeUnified != clock.MustUnified(gs.TimeStartTicks, 90000) {
			t.Fatalf("sample %d unified time=%d want %d", i, got.TimeUnified,
				clock.MustUnified(gs.TimeStartTicks, 90000))
		}
	}
}

func sampleCount(r *Result) int {
	if len(r.Tracks) == 0 {
		return 0
	}
	return len(r.Tracks[0].Samples)
}

func TestTruncatedBoxLocatesDamagedSample(t *testing.T) {
	spec := fixture.ProgressiveSpec{Timescale: 90000, Seed: 7}
	const n = 40
	for i := 0; i < n; i++ {
		spec.Samples = append(spec.Samples, fixture.SampleSpec{Delta: 3000, Size: 4096, Sync: i%10 == 0})
	}
	built, _ := fixture.BuildProgressive(spec)
	const victim = 25
	gt := built.Samples[victim]
	cut := gt.Offset + int64(2048) // half-cut inside victim sample
	if cut >= int64(len(built.Bytes)) {
		t.Fatal("cut beyond fixture")
	}
	p := writeTemp(t, "trunc.mp4", built.Bytes[:cut])

	res, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	var tf *model.Fault
	for i := range res.Faults {
		if res.Faults[i].Code == model.CodeTruncatedBox {
			tf = &res.Faults[i]
			break
		}
	}
	if tf == nil {
		t.Fatalf("expected truncated_box fault, got %+v", res.Faults)
	}
	if tf.OffsetBytes != cut {
		t.Fatalf("fault offset=%d want EOF/cut=%d", tf.OffsetBytes, cut)
	}
	if tf.SampleIndex != victim {
		t.Fatalf("sample index=%d want %d", tf.SampleIndex, victim)
	}
	wantTime := clock.MustUnified(gt.TimeStartTicks, 90000)
	if tf.TimeStart != wantTime {
		t.Fatalf("fault time=%d want sample start %d", tf.TimeStart, wantTime)
	}
	// The cut is inside exactly the reported sample interval.
	if !(gt.Offset <= cut && cut < gt.Offset+int64(gt.Size)) {
		t.Fatal("cut not within victim sample")
	}
}

func TestIllegalBoxLengthReported(t *testing.T) {
	b := []byte{0, 0, 0, 4, 'm', 'd', 'a', 't'} // size 4 < 8
	p := writeTemp(t, "bad.mp4", b)
	res, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range res.Faults {
		if f.Code == model.CodeIllegalBoxLength && f.OffsetBytes == 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected illegal_box_length@0, got %+v", res.Faults)
	}
}

func TestMissingInitialization(t *testing.T) {
	p := writeTemp(t, "zero.mp4", make([]byte, 64))
	res, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range res.Faults {
		if f.Code == model.CodeMissingInit {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected missing_initialization, got %+v", res.Faults)
	}
}

func TestFragmentedInitAndMedia(t *testing.T) {
	initBytes := fixture.BuildFragmentedInit(90000)
	initPath := writeTemp(t, "init.mp4", initBytes)
	initRes, err := Inspect(initPath)
	if err != nil {
		t.Fatal(err)
	}
	if initRes.Kind != "initialization" || initRes.Timescale != 90000 {
		t.Fatalf("init kind=%s ts=%d faults=%+v", initRes.Kind, initRes.Timescale, initRes.Faults)
	}

	deltas, sizes := []uint32{3000, 3000, 3000}, []uint32{100, 200, 300}
	fragBytes := fixture.BuildMediaFragment(fixture.FragmentSpec{
		Sequence: 1, BaseTime: 0xFFFFFF00, Deltas: deltas, Sizes: sizes, Seed: 9,
	})
	fragPath := writeTemp(t, "seg1.m4s", fragBytes)
	res, err := InspectWith(fragPath, Options{FallbackTimescale: 90000})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != "fragmented" || len(res.Fragments) != 1 {
		t.Fatalf("kind=%s frags=%d", res.Kind, len(res.Fragments))
	}
	fr := res.Fragments[0]
	if fr.BaseDecodeRaw != 0xFFFFFF00 || fr.Sequence != 1 {
		t.Fatalf("base=%x seq=%d", fr.BaseDecodeRaw, fr.Sequence)
	}
	if len(fr.Samples) != 3 || fr.Samples[2].Size != 300 {
		t.Fatalf("fragment samples=%+v", fr.Samples)
	}
	if !fr.Samples[0].Sync || fr.Samples[1].Sync {
		t.Fatal("only first fragment sample should be marked sync")
	}
}

func TestGeneratedMP4IsGoMP4Compatible(t *testing.T) {
	spec := fixture.ProgressiveSpec{Timescale: 48000}
	for i := 0; i < 12; i++ {
		spec.Samples = append(spec.Samples, fixture.SampleSpec{Delta: 1024, Size: 128, Sync: i%4 == 0})
	}
	built, _ := fixture.BuildProgressive(spec)
	p := writeTemp(t, "compat.mp4", built.Bytes)
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	saw := map[string]bool{}
	_, err = gomp4.ReadBoxStructure(f, func(h *gomp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type.String() {
		case "moov", "trak", "mdia", "minf", "stbl":
			return h.Expand()
		case "stts", "stsz", "stco":
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			saw[h.BoxInfo.Type.String()] = box != nil
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("go-mp4 rejected generated structure: %v", err)
	}
	for _, typ := range []string{"stts", "stsz", "stco"} {
		if !saw[typ] {
			t.Fatalf("go-mp4 did not parse %s", typ)
		}
	}
}
