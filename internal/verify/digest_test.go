package verify

import (
	"bytes"
	"strings"
	"testing"

	"mediapack/internal/demux"
	"mediapack/internal/fault"
	"mediapack/internal/fixture"
)

func openPackets(t *testing.T, corr fixture.CorruptKind) ([]*demux.Packet, *demux.Demuxer, error) {
	t.Helper()
	spec := fixture.Spec{
		Seed: 7, Codecs: []string{"V_VP9", "A_OPUS"},
		Clusters: 3, FramesPerTrack: 2, PayloadMin: 24, PayloadMax: 48,
	}
	raw, _ := fixture.Build(spec, corr)
	d, err := demux.Open(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	var pkts []*demux.Packet
	streamErr := d.Stream(func(p *demux.Packet) error {
		pkts = append(pkts, p)
		return nil
	})
	return pkts, d, streamErr
}

func TestDigestDeterminism(t *testing.T) {
	pkts, d, err := openPackets(t, fixture.CorruptNone)
	if err != nil {
		t.Fatal(err)
	}
	build := func() *Report {
		acc := NewAccumulator(d.Mapping())
		for _, p := range pkts {
			acc.Add(p)
		}
		return acc.Finalize(nil)
	}
	a, b := build(), build()
	if !a.Equal(b) {
		t.Fatal("identical runs produced different reports")
	}
	if a.Render() != b.Render() {
		t.Fatal("rendered report text not deterministic")
	}
	if a.BlockCount != len(pkts) {
		t.Fatalf("block count=%d want %d", a.BlockCount, len(pkts))
	}
}

func TestPerTrackDigests(t *testing.T) {
	pkts, d, err := openPackets(t, fixture.CorruptNone)
	if err != nil {
		t.Fatal(err)
	}
	acc := NewAccumulator(d.Mapping())
	for _, p := range pkts {
		acc.Add(p)
	}
	rep := acc.Finalize(nil)
	if len(rep.Tracks) != 2 {
		t.Fatalf("want 2 track digests, got %d", len(rep.Tracks))
	}
	// Blocks are split evenly across the two tracks.
	half := len(pkts) / 2
	for _, td := range rep.Tracks {
		if td.Blocks != half {
			t.Fatalf("track %d blocks=%d want %d", td.OutputTrack, td.Blocks, half)
		}
		if len(td.PayloadSHA) != 64 {
			t.Fatalf("track sha not hex-256: %q", td.PayloadSHA)
		}
	}
	rendered := rep.Render()
	if !strings.Contains(rendered, "track in=1 -> out=1") ||
		!strings.Contains(rendered, "payload_sha256=") {
		t.Fatalf("report missing mapping or aggregate hash:\n%s", rendered)
	}
}

func TestFaultLocationInReport(t *testing.T) {
	pkts, d, streamErr := openPackets(t, fixture.CorruptTruncate)
	if !fault.Is(streamErr, fault.KindTruncated) {
		t.Fatalf("expected truncation, got %v", streamErr)
	}
	acc := NewAccumulator(d.Mapping())
	for _, p := range pkts {
		acc.Add(p)
	}
	rep := acc.Finalize(streamErr)
	if rep.FaultKind != string(fault.KindTruncated) || !rep.Recoverable {
		t.Fatalf("fault not recorded: %+v", rep)
	}
	if rep.FaultOffset < 0 {
		t.Fatal("fault offset missing")
	}
	if !strings.Contains(rep.Render(), "fault=truncated") {
		t.Fatal("rendered report must include fault line")
	}
	// Partial processing is reflected: fewer blocks than a clean run.
	cleanPkts, _, _ := openPackets(t, fixture.CorruptNone)
	if rep.BlockCount >= len(cleanPkts) {
		t.Fatalf("partial run block count %d not below clean %d",
			rep.BlockCount, len(cleanPkts))
	}
}

func TestAggregateHashChangesWithContent(t *testing.T) {
	pkts, d, _ := openPackets(t, fixture.CorruptNone)
	acc1 := NewAccumulator(d.Mapping())
	for _, p := range pkts {
		acc1.Add(p)
	}
	rep1 := acc1.Finalize(nil)

	acc2 := NewAccumulator(d.Mapping())
	for i, p := range pkts {
		if i == 0 {
			mutated := *p
			mutated.Payload = append([]byte(nil), p.Payload...)
			mutated.Payload[0] ^= 0xFF
			acc2.Add(&mutated)
			continue
		}
		acc2.Add(p)
	}
	rep2 := acc2.Finalize(nil)
	if rep1.PayloadSHA256 == rep2.PayloadSHA256 {
		t.Fatal("a one-byte payload change must alter the aggregate hash")
	}
}
