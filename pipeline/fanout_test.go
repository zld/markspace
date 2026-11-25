package pipeline

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/rtp"

	"rtpreplay/ingest"
	"rtpreplay/internal/rtplog"
	"rtpreplay/media"
)

func mustStream(t *testing.T, seqTS [][2]uint32, rate uint32, kbps int) *ingest.Stream {
	t.Helper()
	var buf bytes.Buffer
	if err := rtplog.WriteHeader(&buf); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0).UTC()
	for i, st := range seqTS {
		p := rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 8,
				SequenceNumber: uint16(st[0]), Timestamp: st[1], SSRC: 1,
			},
			Payload: []byte{1, 2},
		}
		raw, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err := rtplog.WriteRecord(&buf, base.Add(time.Duration(i)*20*time.Millisecond), raw); err != nil {
			t.Fatal(err)
		}
	}
	s, err := ingest.ParseRTPStream(bytes.NewReader(buf.Bytes()), int64(buf.Len()), rate, kbps)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFanoutIndependentRecovery(t *testing.T) {
	// Same logical gap in two tiers; each must interpolate on its own clock.
	mk := func(kbps int) *ingest.Stream {
		return mustStream(t, [][2]uint32{
			{0, 0}, {1, 160},
			{4, 640}, // gap seqs 2,3 (3 missing)
			{5, 800},
		}, media.AudioClockRate, kbps)
	}
	reps, err := Fanout([]*ingest.Stream{mk(16), mk(32)}, KindAudio, DefaultMaxGap)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 2 {
		t.Fatalf("reps = %d", len(reps))
	}
	for i, r := range reps {
		// Seqs 2 and 3 are missing between observed seqs 1 and 4.
		if r.MissingCount() != 2 || r.RecoveredCount() != 2 {
			t.Fatalf("rep %d: missing=%d recovered=%d", i, r.MissingCount(), r.RecoveredCount())
		}
		// Linear interpolation at ticks 320 (40 ms) and 480 (60 ms).
		want := []float64{40, 60}
		got := []float64{r.Recovered[0].TimeMS, r.Recovered[1].TimeMS}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("rep %d recovered[%d]=%v want %v", i, j, got, want)
			}
		}
		if r.Recovered[0].BetweenRec != [2]int{1, 2} {
			t.Fatalf("bracketing records wrong: %+v", r.Recovered[0])
		}
	}
	// Recovery state must not alias between representations.
	reps[0].Recovered[0].TimeMS = -1
	if reps[1].Recovered[0].TimeMS == -1 {
		t.Fatal("recovered packets leaked across representations")
	}
	if got := LossRecoveryRate(reps); got != 1.0 {
		t.Fatalf("recovery rate = %f, want 1.0", got)
	}
}

func TestFanoutUnrecoverableHole(t *testing.T) {
	// Gap of 5 with maxGap 2: all five stay unrecoverable.
	st := mustStream(t, [][2]uint32{{0, 0}, {6, 960}}, media.AudioClockRate, 16)
	reps, err := Fanout([]*ingest.Stream{st}, KindAudio, 2)
	if err != nil {
		t.Fatal(err)
	}
	r := reps[0]
	if r.MissingCount() != 5 || r.RecoveredCount() != 0 || len(r.Unrecoverable) != 5 {
		t.Fatalf("missing=%d recovered=%d unrecoverable=%d",
			r.MissingCount(), r.RecoveredCount(), len(r.Unrecoverable))
	}
	if got := LossRecoveryRate(reps); got != 0 {
		t.Fatalf("rate = %f, want 0", got)
	}
}

func TestFanoutNoLossRateIsOne(t *testing.T) {
	st := mustStream(t, [][2]uint32{{0, 0}, {1, 160}, {2, 320}}, media.AudioClockRate, 16)
	reps, err := Fanout([]*ingest.Stream{st}, KindAudio, DefaultMaxGap)
	if err != nil {
		t.Fatal(err)
	}
	if LossRecoveryRate(reps) != 1.0 || reps[0].MissingCount() != 0 {
		t.Fatal("pristine stream must report rate 1.0")
	}
}

func TestLocateBoundariesVFR(t *testing.T) {
	// Irregular video frame spacing (VFR): boundaries must snap to samples.
	frames := [][2]uint32{
		{0, 0}, {1, 3000}, {2, 4000}, {3, 10000},
		{4, 10500}, {5, 20000}, {6, 20200},
	}
	st := mustStream(t, frames, media.VideoClockRate, 64)
	reps, _ := Fanout([]*ingest.Stream{st}, KindVideo, DefaultMaxGap)
	r := reps[0]
	r.LocateBoundaries(50) // 50 ms = 4500 ticks at 90 kHz
	wantMS := []float64{
		0,
		44.44444444444444,  // 4000 ticks
		111.11111111111111, // 10000 ticks
		116.66666666666667, // 10500 ticks
		222.22222222222223, // 20000 ticks
	}
	if len(r.Boundaries) != len(wantMS) {
		t.Fatalf("boundaries = %+v", r.Boundaries)
	}
	for i, b := range r.Boundaries {
		if d := b.TimeMS - wantMS[i]; d > 0.01 || d < -0.01 {
			t.Fatalf("boundary %d: %f want %f", i, b.TimeMS, wantMS[i])
		}
	}
	if r.Boundaries[1].FirstRecord != 2 { // snapped to frame at 4000 ticks
		t.Fatalf("boundary must snap to nearest VFR sample: %+v", r.Boundaries[1])
	}
}

func TestFanoutUnsupportedRate(t *testing.T) {
	st := mustStream(t, [][2]uint32{{0, 0}}, media.AudioClockRate, 16)
	st.ClockRate = 44100
	if _, err := Fanout([]*ingest.Stream{st}, KindAudio, DefaultMaxGap); err == nil {
		t.Fatal("unsupported rate must error")
	}
}
