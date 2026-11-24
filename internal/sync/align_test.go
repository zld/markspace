package sync

import (
	"testing"

	"mediacheck/internal/damage"
	"mediacheck/internal/media"
)

// line builds a track at 1 tick=1ms with a constant per-sample duration.
func line(t *testing.T, id uint32, kind media.SampleKind, count int, startMs, durMs int64, offsetMs int64) *media.TrackLine {
	t.Helper()
	tb, _ := media.NewTimeBase(1000)
	raw := make([]media.RawSample, count)
	for i := 0; i < count; i++ {
		raw[i] = media.RawSample{
			MediaTick:    startMs + int64(i)*durMs + offsetMs,
			DurationTick: uint32(durMs),
			IsSync:       kind == media.KindVideo && i%10 == 0,
			Source:       media.SourceDecoded,
		}
	}
	l, err := media.BuildTrackLine(id, kind, tb, raw, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestAlignPerfectSync(t *testing.T) {
	v := line(t, 1, media.KindVideo, 100, 0, 40, 0)
	a := line(t, 2, media.KindAudio, 100, 0, 40, 0)
	rep := Align(v, a, nil)
	if rep.AbsMeanMs != 0 || rep.MaxAbsMs != 0 || rep.P95AbsMs != 0 {
		t.Fatalf("identical timelines must show zero error, got %+v", rep)
	}
	if len(rep.Measurements) != 100 {
		t.Fatal("every video point must produce a measurement")
	}
}

func TestAlignConstantOffset(t *testing.T) {
	v := line(t, 1, media.KindVideo, 100, 0, 40, 0)
	// Audio leads video by 15 ms on every point.
	a := line(t, 2, media.KindAudio, 100, 0, 40, 15)
	rep := Align(v, a, nil)
	if rep.AbsMeanMs != 15 {
		t.Errorf("abs mean = %f, want 15 ms", rep.AbsMeanMs)
	}
	if rep.MeanErrorMs != 15 {
		t.Errorf("signed mean = %f, want +15 ms (audio ahead)", rep.MeanErrorMs)
	}
	if !rep.WithinTolerance(20) {
		t.Error("15 ms offset must be within a 20 ms budget")
	}
	if rep.WithinTolerance(10) {
		t.Error("15 ms offset must exceed a 10 ms budget")
	}
}

func TestAlignKeepsDamageMeasurements(t *testing.T) {
	v := line(t, 1, media.KindVideo, 100, 0, 40, 0)
	a := line(t, 2, media.KindAudio, 100, 0, 40, 0)
	// Damage covers video samples 50..74 (2000..3000 ms).
	damages := []damage.Range{{StartNanos: 2_000_000_000, EndNanos: 3_000_000_000, Recoverable: damage.RecoverableNo}}
	rep := Align(v, a, damages)
	if rep.DamageMeasurements == 0 {
		t.Fatal("unrecoverable region must contribute explicit measurements")
	}
	if rep.DamageMeasurements != 25 {
		t.Errorf("damage measurements = %d, want 25", rep.DamageMeasurements)
	}
	if len(rep.Measurements) != 100 {
		t.Fatal("damage samples must not be removed from the statistic set")
	}
}

func TestAlignVFRAudioNearest(t *testing.T) {
	// VFR video at irregular cadence; dense audio every 10 ms.
	tb, _ := media.NewTimeBase(1000)
	vStarts := []int64{0, 40, 45, 120, 125}
	vraw := make([]media.RawSample, len(vStarts))
	for i, s := range vStarts {
		vraw[i] = media.RawSample{MediaTick: s, DurationTick: 40, Source: media.SourceDecoded, IsSync: i == 0}
	}
	v, _ := media.BuildTrackLine(1, media.KindVideo, tb, vraw, nil, false)
	a := line(t, 2, media.KindAudio, 200, 0, 10, 0)
	rep := Align(v, a, nil)
	// Video point at 45 ms must snap to audio at 40 or 50 (both 5 ms away).
	if got := rep.Measurements[2].ErrorNanos / 1e6; got != -5 && got != 5 {
		t.Errorf("nearest-audio offset = %d ms, want +/-5", got)
	}
}
