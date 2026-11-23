package archive

import (
	"testing"

	"mediaarchive/internal/ebml"
	"mediaarchive/internal/rtp"
	"mediaarchive/internal/timeline"
)

func TestVerifyArchiveIndexRoundTrip(t *testing.T) {
	index, err := BuildArchiveIndex()
	if err != nil {
		t.Fatalf("build index: %v", err)
	}
	if len(index) == 0 {
		t.Fatal("empty index")
	}
	rep, err := VerifyArchiveIndex(index)
	if err != nil {
		t.Fatalf("verify index: %v", err)
	}
	if !rep.HasFtyp || !rep.HasMoov || !rep.HasMvhd || rep.BoxCount < 3 {
		t.Fatalf("index report wrong: %+v", rep)
	}
}

func TestVerifyRejectsCorruptIndex(t *testing.T) {
	corrupt := []byte{0x00, 0x00, 0x00, 0x08, 'f', 'r', 'e', 'e'}
	if _, err := VerifyArchiveIndex(corrupt); err == nil {
		t.Fatal("corrupt index without ftyp/moov must fail")
	}
}

func TestRecoveredMatroskaReparses(t *testing.T) {
	fx := BuildFixtures(41)
	r, err := ebml.NewReader(fx.HealthyMatroska, ebml.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Parse()
	if err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	if len(res.Events) == 0 {
		t.Fatal("no events in healthy fixture")
	}
	out := WriteRecoveredMatroska(toEvents(res.Events))
	r2, err := ebml.NewReader(out, ebml.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	res2, err := r2.Parse()
	if err != nil {
		t.Fatalf("recovered output must reparse: %v", err)
	}
	if len(res2.Events) != len(res.Events) {
		t.Fatalf("reparsed count=%d want %d", len(res2.Events), len(res.Events))
	}
}

func TestDamagedFixtureReportsTruncation(t *testing.T) {
	fx := BuildFixtures(41)
	r, err := ebml.NewReader(fx.DamagedMatroska, ebml.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Parse()
	if err != nil {
		t.Fatalf("damaged file should be recoverable, got %v", err)
	}
	if len(res.Report.TruncatedClusterOffsets) == 0 {
		t.Fatal("truncated cluster must be reported across parse stage")
	}
}

func TestRTPLossRecoveryCounters(t *testing.T) {
	fx := BuildFixtures(41)
	stream, err := rtp.Recover(fx.RTPPackets,
		rtp.Config{Window: 16, Policy: rtp.PolicyPLC}, 90000,
		fx.ExpectedRTPCount, rtp.ClockOffsetTicks)
	if err != nil {
		t.Fatal(err)
	}
	if stream.Stats.RecoveredPackets != 3 {
		t.Fatalf("recovered=%d want 3", stream.Stats.RecoveredPackets)
	}
	if stream.Stats.UnrecoveredPackets != 1 {
		t.Fatalf("unrecovered=%d want 1", stream.Stats.UnrecoveredPackets)
	}
	if err := stream.IntegrityError(); !timelineErrIs(err, timeline.ErrGap) {
		t.Fatalf("want gap error, got %v", err)
	}
}

func TestLayoutConflictIsolatesTrack(t *testing.T) {
	fx := BuildFixtures(41)
	out, err := Run(Inputs{
		Seed: 41, InputFile: "fixture.mkv",
		Matroska:  fx.HealthyMatroska,
		RawTracks: fx.RawTracks,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Isolated) != 1 || out.Isolated[0].TrackID != 3 {
		t.Fatalf("isolation wrong: %+v", out.Isolated)
	}
	found := false
	for _, id := range out.Manifest.IsolatedTracks {
		if id == 3 {
			found = true
		}
	}
	if !found {
		t.Fatal("isolated track missing from manifest")
	}
}

func TestFullRunInvariantsAndDeterminism(t *testing.T) {
	fx := BuildFixtures(41)
	in := Inputs{
		Seed: 41, InputFile: "fixture.mkv",
		Matroska:         fx.HealthyMatroska,
		RTPPackets:       fx.RTPPackets,
		RTPConfig:        rtp.Config{Window: 16, Policy: rtp.PolicyPLC},
		RTPRate:          90000,
		ExpectedRTPCount: fx.ExpectedRTPCount,
		RTPClockOffset:   rtp.ClockOffsetTicks,
		RawTracks:        fx.RawTracks,
	}
	first, err := Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Invariants(); err != nil {
		t.Fatalf("invariants: %v", err)
	}
	h1, err := HashManifest(first.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	// A second independent run from the same inputs must be byte-identical.
	second, err := Run(in)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashManifest(second.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("manifest not deterministic: %s vs %s", h1, h2)
	}
	if HashBytes(first.OutputBytes) != HashBytes(second.OutputBytes) {
		t.Fatal("recovered output not byte-identical across runs")
	}
}

func TestCrossStageDamageReflectedInManifest(t *testing.T) {
	fx := BuildFixtures(41)
	out, err := Run(Inputs{
		Seed: 41, InputFile: "damaged.mkv",
		Matroska:         fx.DamagedMatroska,
		RTPPackets:       fx.RTPPackets,
		RTPConfig:        rtp.Config{Window: 16, Policy: rtp.PolicyPLC},
		RTPRate:          90000,
		ExpectedRTPCount: fx.ExpectedRTPCount,
		RTPClockOffset:   rtp.ClockOffsetTicks,
		RawTracks:        fx.RawTracks,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Manifest.TruncatedOffsets) == 0 {
		t.Fatal("parse-stage truncation missing in manifest")
	}
	if out.Manifest.RTP.UnrecoveredPackets != 1 {
		t.Fatal("sync-stage RTP loss missing in manifest")
	}
	if len(out.Manifest.IsolatedTracks) == 0 {
		t.Fatal("layout-stage isolation missing in manifest")
	}
}
