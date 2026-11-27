package fixture

import (
	"bytes"
	"testing"

	"tsrepair/internal/mediaerr"
	"tsrepair/internal/ts"
)

// parseStream runs the real TS reader and PSI tracker over a generated
// representation, returning the collected errors.
func parseStream(t *testing.T, data []byte) *mediaerr.Collector {
	t.Helper()
	ec := &mediaerr.Collector{}
	r := ts.NewReader(bytes.NewReader(data), ec)
	tracker := ts.NewPSITracker(ec)
	for {
		p, err := r.ReadPacket()
		if err != nil {
			break
		}
		tracker.HandlePacket(p)
	}
	return ec
}

func TestBuildCleanStreamParses(t *testing.T) {
	opts := DefaultOptions(17)
	opts.Segments = 3
	opts.SamplesPerSegment = 4
	reps, err := Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 1 {
		t.Fatalf("reps=%d", len(reps))
	}
	rep := reps[0]
	if len(rep.ExpectedPTS90k) != 12 {
		t.Errorf("expected 12 samples, got %d", len(rep.ExpectedPTS90k))
	}
	if len(rep.KeyframeSamples) != 3 {
		t.Errorf("keyframes=%d want 3", len(rep.KeyframeSamples))
	}
	ec := parseStream(t, rep.Data)
	if len(ec.All()) != 0 {
		t.Fatalf("clean stream produced errors: %v", ec.All())
	}
}

func TestDeterministicGeneration(t *testing.T) {
	opts := DefaultOptions(17)
	a, _ := Build(opts)
	b, _ := Build(opts)
	if !bytes.Equal(a[0].Data, b[0].Data) {
		t.Fatal("same seed must produce identical bytes")
	}
}

func TestMultiRepresentationSharedTiming(t *testing.T) {
	opts := DefaultOptions(17)
	opts.Representations = 3
	reps, err := Build(opts)
	if err != nil || len(reps) != 3 {
		t.Fatalf("reps=%d err=%v", len(reps), err)
	}
	for i := 1; i < len(reps); i++ {
		if !equalInt64(reps[0].ExpectedPTS90k, reps[i].ExpectedPTS90k) {
			t.Fatal("representations must share media timing")
		}
	}
}

func TestCounterGapDetected(t *testing.T) {
	opts := DefaultOptions(1)
	opts.Corruptions = []Corruption{CorruptCounterGap}
	reps, _ := Build(opts)
	ec := parseStream(t, reps[0].Data)
	if ec.CountByCode(mediaerr.CodeCCGap) == 0 {
		t.Fatalf("expected CC gap, got %v", ec.All())
	}
}

func TestPSIVersionRollbackDetected(t *testing.T) {
	opts := DefaultOptions(1)
	opts.Corruptions = []Corruption{CorruptPSIVersion}
	reps, _ := Build(opts)
	ec := parseStream(t, reps[0].Data)
	if ec.CountByCode(mediaerr.CodeVersionRollback) == 0 {
		t.Fatalf("expected version rollback, got %v", ec.All())
	}
}

func TestPSIMissingSectionDetected(t *testing.T) {
	opts := DefaultOptions(1)
	opts.Corruptions = []Corruption{CorruptPSIMissingSection}
	reps, _ := Build(opts)
	ec := parseStream(t, reps[0].Data)
	if ec.CountByCode(mediaerr.CodeSectionMissing) == 0 {
		t.Fatalf("expected missing section, got %v", ec.All())
	}
}

func TestTruncatedPacketDetected(t *testing.T) {
	opts := DefaultOptions(1)
	opts.Corruptions = []Corruption{CorruptTruncatedPacket}
	reps, _ := Build(opts)
	ec := parseStream(t, reps[0].Data)
	// The short tail either breaks sync or ends the reader mid-packet.
	if ec.CountByCode(mediaerr.CodeSyncLost) == 0 && ec.CountByCode(mediaerr.CodeTruncatedPacket) == 0 {
		t.Fatalf("expected sync/truncation error, got %v", ec.All())
	}
}

func TestMissingPCRAnchorOmitsPCR(t *testing.T) {
	opts := DefaultOptions(1)
	opts.Corruptions = []Corruption{CorruptMissingPCRAnchor}
	reps, _ := Build(opts)
	if len(reps[0].PCRRaw) != 0 {
		t.Fatal("PCR must be omitted")
	}
	// The stream itself still parses structurally.
	ec := parseStream(t, reps[0].Data)
	for _, e := range ec.All() {
		if e.Severity == mediaerr.SeverityFatal {
			t.Fatalf("missing PCR must not be structurally fatal: %v", e)
		}
	}
}

func TestPCRWrapValuesCrossModulus(t *testing.T) {
	opts := DefaultOptions(1)
	opts.Corruptions = []Corruption{CorruptPCRWrap}
	reps, _ := Build(opts)
	raw := reps[0].PCRRaw
	last := raw[len(raw)-1]
	prev := raw[len(raw)-2]
	if !(prev > last) {
		t.Fatalf("expected wrapped final PCR, prev=%d last=%d", prev, last)
	}
}

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
