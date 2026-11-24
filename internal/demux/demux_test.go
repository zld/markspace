package demux

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"mediapack/internal/fault"
	"mediapack/internal/fixture"
	"mediapack/internal/track"
)

func spec() fixture.Spec {
	return fixture.Spec{
		Seed: 7, Codecs: []string{"V_VP9", "A_OPUS", "S_TEXT/WEBVTT"},
		Clusters: 3, FramesPerTrack: 2, PayloadMin: 24, PayloadMax: 48,
	}
}

func TestOpenAndMapping(t *testing.T) {
	raw, _ := fixture.Build(spec(), fixture.CorruptNone)
	d, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	m := d.Mapping()
	if len(m.Tracks) != 3 {
		t.Fatalf("want 3 tracks, got %d", len(m.Tracks))
	}
	want := []struct {
		in, out uint32
		codec   string
		kind    track.Kind
	}{
		{1, 1, "V_VP9", track.KindVideo},
		{2, 2, "A_OPUS", track.KindAudio},
		{3, 3, "S_TEXT/WEBVTT", track.KindSubtitle},
	}
	for i, w := range want {
		got := m.Tracks[i]
		if got.InputNumber != w.in || got.OutputIndex != w.out ||
			got.Codec.CodecID != w.codec || got.Codec.Kind != w.kind {
			t.Fatalf("track %d mismatch: %+v", i, got)
		}
	}
	if d.TimestampScaleNS() != 1_000_000 {
		t.Fatalf("timestamp scale=%d", d.TimestampScaleNS())
	}
}

func TestStreamOrderPayloadAndOffsets(t *testing.T) {
	raw, detail := fixture.Build(spec(), fixture.CorruptNone)
	d, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var packets []*Packet
	if err := d.Stream(func(p *Packet) error {
		packets = append(packets, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(packets) != detail.BlockCount {
		t.Fatalf("packet count=%d want %d", len(packets), detail.BlockCount)
	}
	// Sequence numbers are dense and every payload matches its input range.
	for i, p := range packets {
		if p.Sequence != i {
			t.Fatalf("sequence gap at %d: %d", i, p.Sequence)
		}
		want := raw[p.InputStart:p.InputEnd]
		if !bytes.Equal(p.Payload, want) {
			t.Fatalf("packet %d payload not byte-identical to input range", i)
		}
		if len(p.Payload) == 0 {
			t.Fatalf("packet %d has empty payload", i)
		}
		// Output index follows the stable mapping.
		if p.OutputTrack != p.InputTrack {
			t.Fatalf("1:1 fixture mapping violated: in=%d out=%d",
				p.InputTrack, p.OutputTrack)
		}
	}
	// Nanosecond timestamps: first cluster at tick 0 with 1ms scale.
	if packets[0].TimestampNS != 0 {
		t.Fatalf("first timestamp=%d want 0", packets[0].TimestampNS)
	}
}

func TestPayloadHashesStable(t *testing.T) {
	raw, _ := fixture.Build(spec(), fixture.CorruptNone)
	hash := func() [32]byte {
		d, err := Open(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		if err := d.Stream(func(p *Packet) error {
			h.Write(p.Payload)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var out [32]byte
		copy(out[:], h.Sum(nil))
		return out
	}
	if hash() != hash() {
		t.Fatal("identical input produced different payload digests")
	}
}

func TestUnsupportedCodecFaultHasOffset(t *testing.T) {
	s := spec()
	s.Codecs = []string{"V_VP9", "V_MPEG4/ISO/AVC"}
	raw, _ := fixture.Build(s, fixture.CorruptNone)
	_, err := Open(bytes.NewReader(raw))
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindUnsupportedCodec {
		t.Fatalf("expected unsupported-codec fault, got %v", err)
	}
	if fe.Offset < 0 || fe.Track != 2 {
		t.Fatalf("fault must carry track 2 and an input offset, got %v", fe)
	}
}

func TestTimestampRegressionPropagates(t *testing.T) {
	raw, _ := fixture.Build(spec(), fixture.CorruptTimestampRegression)
	d, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	err = d.Stream(func(p *Packet) error { return nil })
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindTimestampRegression || !fe.Recoverable {
		t.Fatalf("expected recoverable regression fault, got %v", err)
	}
	if fe.Offset < 0 || fe.Track <= 0 {
		t.Fatalf("regression fault must locate track and offset, got %v", fe)
	}
}

func TestTruncatedInputStopsWithOffset(t *testing.T) {
	raw, detail := fixture.Build(spec(), fixture.CorruptTruncate)
	// The full (uncorrupted) stream defines how many blocks would exist
	// without truncation; the truncated stream can only reach a strict subset.
	_, full := fixture.Build(spec(), fixture.CorruptNone)
	d, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	err = d.Stream(func(p *Packet) error {
		count++
		return nil
	})
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindTruncated || !fe.Recoverable {
		t.Fatalf("expected recoverable truncation, got %v", err)
	}
	if delta := fe.Offset - detail.CorruptOffset; delta < -1 || delta > 1 {
		t.Fatalf("truncation offset %d not within 1 byte of cut %d",
			fe.Offset, detail.CorruptOffset)
	}
	// Only fully-readable blocks before the cut were delivered.
	if count == 0 || count >= full.BlockCount {
		t.Fatalf("delivered %d of %d full-stream blocks before fault",
			count, full.BlockCount)
	}
	// Every delivered packet must be complete (none spans the cut).
	if count != detail.BlockCount {
		t.Fatalf("delivered %d but scanner found %d reachable blocks",
			count, detail.BlockCount)
	}
}

func TestAllSupportedCodecsDemux(t *testing.T) {
	s := fixture.Spec{
		Seed: 11, Codecs: []string{"V_VP8", "V_VP9", "A_OPUS", "A_VORBIS", "S_TEXT/WEBVTT"},
		Clusters: 2, FramesPerTrack: 1, PayloadMin: 16, PayloadMax: 16,
	}
	raw, detail := fixture.Build(s, fixture.CorruptNone)
	d, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	if err := d.Stream(func(p *Packet) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	if n != detail.BlockCount || n != 10 {
		t.Fatalf("demuxed %d packets, want 10", n)
	}
}
