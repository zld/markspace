package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediatrace/internal/audio"
	"mediatrace/internal/timeline"
)

// fakeFFmpeg records invocations and writes real (small) output files so the
// rest of the pipeline can stat and hash them. Gate outcomes are scripted
// per bitrate to exercise the quality-first selection.
type fakeFFmpeg struct {
	gateByRate map[int]audio.LoudnessStats
	decodeErr  error
	encodeErr  error
	measureErr error
	calls      []string
}

func (f *fakeFFmpeg) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	_ = name
	f.calls = append(f.calls, args[0])
	// Locate the output path (last argument for our fixed arg layout).
	out := args[len(args)-1]
	if filepath.Ext(out) == ".pcm" {
		if f.decodeErr != nil {
			return nil, []byte("decode boom"), f.decodeErr
		}
		return loudnormAnalysis(), nil, nil
	}
	if filepath.Ext(out) == ".m4a" && contains(args, "aac") {
		if f.encodeErr != nil {
			return nil, []byte("encode boom"), f.encodeErr
		}
		if err := os.WriteFile(out, []byte("fake-aac-"+filepath.Base(out)), 0o644); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	}
	if contains(args, "ebur128") {
		if f.measureErr != nil {
			return nil, []byte("measure boom"), f.measureErr
		}
		br := bitrateFromArgs(args)
		stats := f.gateByRate[br]
		return ebur128Summary(stats), nil, nil
	}
	return nil, nil, nil
}

func contains(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

func bitrateFromArgs(args []string) int {
	for i, a := range args {
		if a == "-b:a" && i+1 < len(args) {
			br := 0
			for _, c := range args[i+1] {
				if c >= '0' && c <= '9' {
					br = br*10 + int(c-'0')
				}
			}
			return br
		}
	}
	return 0
}

func loudnormAnalysis() []byte {
	return []byte(`{
	"input_i" : "-20.00",
	"input_tp" : "-10.00",
	"input_lra" : "2.00",
	"input_thresh" : "-30.00",
	"output_i" : "-16.00",
	"output_tp" : "-2.00",
	"output_lra" : "2.00",
	"output_thresh" : "-26.00",
	"normalization_type" : "linear",
	"target_offset" : "0.00"
}
`)
}

func ebur128Summary(s audio.LoudnessStats) []byte {
	return []byte("frame line\n" +
		"[Parsed_ebur128_0 @ 0] t: 1.0 I: " +
		formatFloat(s.Integrated) + " LUFS TPK: " + formatFloat(s.TruePeak) + " dBTP\n")
}

func formatFloat(v float64) string {
	// Minimal fixed-point formatting sufficient for the parser (one decimal).
	whole := int64(v)
	frac := int64((v-float64(whole))*10 + 0.5)
	if frac < 0 {
		frac = -frac
	}
	return itoa(whole) + "." + itoa(frac)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestChooseSmallestPassing(t *testing.T) {
	cs := []candidate{
		{bitrateKbps: 128, sizeBytes: 500, stats: audio.LoudnessStats{Integrated: -16, TruePeak: -2}},
		{bitrateKbps: 96, sizeBytes: 300, stats: audio.LoudnessStats{Integrated: -16, TruePeak: -2}},
		{bitrateKbps: 64, sizeBytes: 150, stats: audio.LoudnessStats{Integrated: -10, TruePeak: 0}}, // fails
	}
	best := chooseSmallestPassing(cs)
	if best == nil || best.bitrateKbps != 96 {
		t.Fatalf("expected 96k smallest passing, got %+v", best)
	}
	if chooseSmallestPassing(cs[2:3]) != nil {
		t.Fatal("all-failing ladder must return nil")
	}
}

func TestPlanLadderBudget(t *testing.T) {
	p := DefaultPlan()
	p.BudgetPasses = 2
	if got := p.ladder(); len(got) != 2 || got[0] != 128 || got[1] != 96 {
		t.Fatalf("ladder=%v", got)
	}
	p.BudgetPasses = 3
	if len(p.ladder()) != 3 {
		t.Fatalf("full ladder=%v", p.ladder())
	}
}

func TestFrameAtRounding(t *testing.T) {
	// 1 second at 48 kHz.
	if got := frameAt(1_000_000_000); got != 48000 {
		t.Fatalf("frameAt(1s)=%d", got)
	}
	// Half a frame rounds consistently.
	if got := frameAt(0); got != 0 {
		t.Fatalf("frameAt(0)=%d", got)
	}
	// Negative timestamps stay symmetric.
	if got := frameAt(-1_000_000_000); got != -48000 {
		t.Fatalf("frameAt(-1s)=%d", got)
	}
}

func TestPlanValidation(t *testing.T) {
	p := DefaultPlan()
	p.BudgetPasses = 0
	if err := p.validate(); err == nil {
		t.Fatal("zero budget must fail validation")
	}
	p = DefaultPlan()
	p.BudgetPasses = 4
	if err := p.validate(); err == nil {
		t.Fatal("budget > 3 must fail validation")
	}
}

func TestBuildRTPObservations(t *testing.T) {
	events := []timeline.Event{
		{Kind: "ssrc_map", SSRC: 7, Track: 1, ClockRate: 48000},
		{Kind: "rtp", SSRC: 7, RTPTS: 0, Sequence: 1, AtNS: 0},
		{Kind: "rtp", SSRC: 7, RTPTS: 24000, Sequence: 2, AtNS: 500_000_000},
		{Kind: "rtp", SSRC: 9, RTPTS: 100, Sequence: 1, AtNS: 1000}, // unmapped SSRC
	}
	got := buildRTPObservations(events)
	if len(got) != 3 {
		t.Fatalf("want 3 RTP observations, got %d", len(got))
	}
	if got[0].TrackNo != 1 || got[0].ConvertedNS != 0 {
		t.Fatalf("anchor wrong: %+v", got[0])
	}
	if got[1].ConvertedNS != 500_000_000 {
		t.Fatalf("conversion wrong: %+v", got[1])
	}
	if got[2].TrackNo != 0 {
		t.Fatalf("unmapped SSRC must keep track 0: %+v", got[2])
	}
}

func TestBuildRTPMarksRegression(t *testing.T) {
	events := []timeline.Event{
		{Kind: "ssrc_map", SSRC: 7, Track: 1, ClockRate: 48000},
		{Kind: "rtp", SSRC: 7, RTPTS: 1000, Sequence: 1, AtNS: 0},
		{Kind: "rtp", SSRC: 7, RTPTS: 900, Sequence: 2, AtNS: 100}, // backwards
	}
	got := buildRTPObservations(events)
	if got[0].Gap != "" {
		t.Fatalf("first packet cannot regress: %+v", got[0])
	}
	if got[1].Gap != string(timeline.GapRegression) {
		t.Fatalf("regression not marked: %+v", got[1])
	}
	// The raw timestamp is preserved, not rewritten.
	if got[1].RTPTS != 900 || got[1].ConvertedNS == got[0].ConvertedNS {
		t.Fatalf("regressed packet values wrong: %+v", got[1])
	}
}
