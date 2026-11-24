package crosscheck

import (
	"bytes"
	"testing"

	"mediapack/internal/demux"
	"mediapack/internal/fixture"
)

func demuxPackets(t *testing.T, spec fixture.Spec) ([]*demux.Packet, uint64) {
	t.Helper()
	raw, _ := fixture.Build(spec, fixture.CorruptNone)
	d, err := demux.Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var pkts []*demux.Packet
	if err := d.Stream(func(p *demux.Packet) error {
		pkts = append(pkts, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return pkts, d.TimestampScaleNS()
}

func TestAgreementWithEBMLGo(t *testing.T) {
	spec := fixture.Spec{
		Seed: 23, Codecs: []string{"V_VP9", "A_OPUS"},
		Clusters: 4, FramesPerTrack: 3, PayloadMin: 20, PayloadMax: 50,
	}
	pkts, scale := demuxPackets(t, spec)

	raw, _ := fixture.Build(spec, fixture.CorruptNone)
	ref, err := DecodeWithEBMLGo(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("reference decoder failed: %v", err)
	}
	if err := CompareDemuxAgainstReference(pkts, ref, scale); err != nil {
		t.Fatalf("cross-decoder disagreement: %v", err)
	}
}

func TestAgreementAllCodecs(t *testing.T) {
	spec := fixture.Spec{
		Seed: 3, Codecs: []string{"V_VP8", "V_VP9", "A_OPUS", "A_VORBIS", "S_TEXT/WEBVTT"},
		Clusters: 2, FramesPerTrack: 2, PayloadMin: 16, PayloadMax: 32,
	}
	pkts, scale := demuxPackets(t, spec)
	raw, _ := fixture.Build(spec, fixture.CorruptNone)
	ref, err := DecodeWithEBMLGo(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("reference decoder failed: %v", err)
	}
	if err := CompareDemuxAgainstReference(pkts, ref, scale); err != nil {
		t.Fatalf("cross-decoder disagreement: %v", err)
	}
}

func TestDetectsPayloadTamper(t *testing.T) {
	spec := fixture.Spec{
		Seed: 5, Codecs: []string{"V_VP9"},
		Clusters: 2, FramesPerTrack: 1, PayloadMin: 24, PayloadMax: 24,
	}
	pkts, scale := demuxPackets(t, spec)
	raw, _ := fixture.Build(spec, fixture.CorruptNone)
	ref, err := DecodeWithEBMLGo(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	// Tamper one internal payload; the cross-check must catch it.
	pkts[0].Payload[0] ^= 0xFF
	if err := CompareDemuxAgainstReference(pkts, ref, scale); err == nil {
		t.Fatal("tampered payload was not detected by cross-check")
	}
}
