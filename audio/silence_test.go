package audio

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/rtp"

	"rtpreplay/ingest"
	"rtpreplay/internal/rtplog"
	"rtpreplay/media"
	"rtpreplay/pipeline"
)

func streamWithPayloads(t *testing.T, payloads [][]byte) *ingest.Stream {
	t.Helper()
	var buf bytes.Buffer
	if err := rtplog.WriteHeader(&buf); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0).UTC()
	for i, pl := range payloads {
		p := rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 8,
				SequenceNumber: uint16(i), Timestamp: uint32(i * 160), SSRC: 1,
			},
			Payload: pl,
		}
		raw, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err := rtplog.WriteRecord(&buf, base.Add(time.Duration(i)*20*time.Millisecond), raw); err != nil {
			t.Fatal(err)
		}
	}
	st, err := ingest.ParseRTPStream(bytes.NewReader(buf.Bytes()), int64(buf.Len()), media.AudioClockRate, 32)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestLevel(t *testing.T) {
	if Level(nil) != 0 || Level([]byte{0, 0, 0}) != 0 {
		t.Fatal("zero payload must have zero level")
	}
	if got := Level([]byte{10, 20, 30}); got != 20 {
		t.Fatalf("level = %f, want 20", got)
	}
	// Signed-style high bytes fold back to small amplitudes.
	if got := Level([]byte{0xFF}); got != 1 {
		t.Fatalf("0xFF amplitude = %f, want 1", got)
	}
}

func TestDetectSilenceRegions(t *testing.T) {
	silence := []byte{0, 0, 0, 0}
	tone := []byte{100, 100, 100, 100}
	payloads := [][]byte{
		silence, silence, // region 1: records 0-1
		tone, tone, // audible
		silence, silence, silence, // region 2: records 4-6
		tone,
	}
	st := streamWithPayloads(t, payloads)
	reps, err := pipeline.Fanout([]*ingest.Stream{st}, pipeline.KindAudio, pipeline.DefaultMaxGap)
	if err != nil {
		t.Fatal(err)
	}
	regions := DetectSilence(reps[0], DefaultLevelThreshold)
	if len(regions) != 2 {
		t.Fatalf("regions = %+v", regions)
	}
	r1, r2 := regions[0], regions[1]
	if r1.StartRecord != 0 || r1.EndRecord != 1 || r1.Packets != 2 {
		t.Fatalf("region 1 wrong: %+v", r1)
	}
	if r1.Threshold != DefaultLevelThreshold {
		t.Fatalf("threshold must be reported: %+v", r1)
	}
	if r1.StartMS != 0 || r1.EndMS != 20 {
		t.Fatalf("region 1 timing wrong: %f..%f", r1.StartMS, r1.EndMS)
	}
	if r2.StartRecord != 4 || r2.EndRecord != 6 || r2.Packets != 3 {
		t.Fatalf("region 2 wrong: %+v", r2)
	}
}

func TestDetectSilenceCustomThreshold(t *testing.T) {
	// Level 3 payloads are silent under threshold 4 but audible under 1.
	quiet := []byte{3, 3, 3, 3}
	st := streamWithPayloads(t, [][]byte{quiet, quiet})
	reps, _ := pipeline.Fanout([]*ingest.Stream{st}, pipeline.KindAudio, pipeline.DefaultMaxGap)
	if len(DetectSilence(reps[0], 4)) != 1 {
		t.Fatal("level 3 must be silent at threshold 4")
	}
	if len(DetectSilence(reps[0], 1)) != 0 {
		t.Fatal("level 3 must be audible at threshold 1")
	}
}

func TestDetectSilenceNoAudibleStream(t *testing.T) {
	st := streamWithPayloads(t, [][]byte{{100}, {100}})
	reps, _ := pipeline.Fanout([]*ingest.Stream{st}, pipeline.KindAudio, pipeline.DefaultMaxGap)
	if regions := DetectSilence(reps[0], DefaultLevelThreshold); len(regions) != 0 {
		t.Fatalf("fully audible stream must have no silence: %+v", regions)
	}
}

func TestDetectSilenceFallsBackToDefault(t *testing.T) {
	st := streamWithPayloads(t, [][]byte{{0}, {0}})
	reps, _ := pipeline.Fanout([]*ingest.Stream{st}, pipeline.KindAudio, pipeline.DefaultMaxGap)
	regions := DetectSilence(reps[0], -1)
	if len(regions) != 1 || regions[0].Threshold != DefaultLevelThreshold {
		t.Fatalf("negative threshold must fall back to default: %+v", regions)
	}
}
