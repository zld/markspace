package audio

import (
	"encoding/binary"
	"testing"

	"mediamerge/internal/model"
)

func pcm(v int16, parts int) []byte {
	b := make([]byte, parts*2)
	for i := 0; i < parts; i++ {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

func sample(startNs int64, durNs int64, data []byte) model.Sample {
	return model.Sample{TrackID: 2, Start: model.NsTime(startNs),
		Dur: model.NsDuration(durNs), Size: uint32(len(data)), SourceFile: "a.mp4"}
}

func payloadMap(m map[int64][]byte) PayloadFunc {
	return func(s model.Sample) ([]byte, bool) {
		b, ok := m[int64(s.Start)]
		return b, ok
	}
}

func TestAnalyzeClassifiesWindows(t *testing.T) {
	const ms = 1_000_000
	// Window 100 ms; samples of 100 ms each.
	samples := []model.Sample{
		sample(0, 100*ms, pcm(0, 4800)),         // all zero
		sample(100*ms, 100*ms, pcm(30, 4800)),   // very quiet: below -60 dBFS
		sample(400*ms, 100*ms, pcm(9000, 4800)), // active, with a gap before it
	}
	payloads := map[int64][]byte{
		0:        pcm(0, 4800),
		100 * ms: pcm(30, 4800),
		400 * ms: pcm(9000, 4800),
	}
	rep := Analyze(2, samples, payloadMap(payloads), Options{Window: 100 * ms, SilenceDBFS: -60})
	if len(rep.Windows) != 5 {
		t.Fatalf("windows=%d", len(rep.Windows))
	}
	kinds := []Kind{KindAllZero, KindBelowThreshold, KindMissing, KindMissing, KindActive}
	for i, k := range kinds {
		if rep.Windows[i].Kind != k {
			t.Fatalf("window %d = %s want %s", i, rep.Windows[i].Kind, k)
		}
	}
	if rep.Stats.Missing != 2 {
		t.Fatalf("missing=%d", rep.Stats.Missing)
	}
	if rep.Stats.Silent != 2 {
		t.Fatalf("silent=%d (missing must not count)", rep.Stats.Silent)
	}
	if got := rep.SilentMs(); got != 200 {
		t.Fatalf("silent_ms=%d", got)
	}
	// Missing windows are reported separately, never inside silent intervals.
	for _, iv := range rep.Intervals {
		if iv.Start >= 200*ms && iv.Start < 400*ms {
			t.Fatalf("gap misreported as silence: %+v", iv)
		}
	}
	if len(rep.Missing) != 1 || rep.Missing[0].Start != 200*ms || rep.Missing[0].End != 400*ms {
		t.Fatalf("missing intervals=%+v", rep.Missing)
	}
	if !rep.Estimated {
		t.Fatal("output must be marked estimated")
	}
	if rep.Stats.PeakDBFS <= rep.Stats.RMSDBFS {
		t.Fatalf("peak %v vs rms %v", rep.Stats.PeakDBFS, rep.Stats.RMSDBFS)
	}
}

func TestMissingPayloadIsNotSilence(t *testing.T) {
	const ms = 1_000_000
	samples := []model.Sample{sample(0, 100*ms, pcm(9000, 4800))}
	rep := Analyze(2, samples, func(model.Sample) ([]byte, bool) { return nil, false },
		Options{Window: 100 * ms})
	if rep.Windows[0].Kind != KindMissing {
		t.Fatalf("kind=%s", rep.Windows[0].Kind)
	}
	if rep.Stats.Silent != 0 || rep.SilentMs() != 0 {
		t.Fatal("unreadable payload treated as silence")
	}
}

func TestPartialCoverageIsMissing(t *testing.T) {
	const ms = 1_000_000
	// 40 ms of signal, a 60 ms hole, then a full second window: the first
	// window is only partly covered and must be reported missing, not
	// active, even though the bytes it does have are loud.
	samples := []model.Sample{
		sample(0, 40*ms, pcm(9000, 1920)),
		sample(100*ms, 100*ms, pcm(9000, 4800)),
	}
	payloads := map[int64][]byte{0: pcm(9000, 1920), 100 * ms: pcm(9000, 4800)}
	rep := Analyze(2, samples, payloadMap(payloads), Options{Window: 100 * ms})
	if len(rep.Windows) != 2 {
		t.Fatalf("windows=%d", len(rep.Windows))
	}
	if rep.Windows[0].Kind != KindMissing {
		t.Fatalf("window 0 kind=%s", rep.Windows[0].Kind)
	}
	if rep.Windows[1].Kind != KindActive {
		t.Fatalf("window 1 kind=%s", rep.Windows[1].Kind)
	}
	if rep.Stats.Silent != 0 {
		t.Fatal("partial window counted as silence")
	}
}

func TestAnalyzeEmptyTrack(t *testing.T) {
	rep := Analyze(2, nil, payloadMap(nil), Options{})
	if rep.Stats.Missing != 1 || rep.SilentMs() != 0 {
		t.Fatalf("stats=%+v", rep.Stats)
	}
}

func TestByteCapIsEnforced(t *testing.T) {
	const ms = 1_000_000
	data := pcm(9000, 4000)
	rep := Analyze(2, []model.Sample{sample(0, 100*ms, data)}, payloadMap(map[int64][]byte{0: data}),
		Options{Window: 100 * ms, MaxBytesPerSample: 1000})
	if rep.Windows[0].Bytes != 1000 {
		t.Fatalf("bytes=%d", rep.Windows[0].Bytes)
	}
}

func TestAnalyzeDeterministic(t *testing.T) {
	const ms = 1_000_000
	samples := []model.Sample{sample(0, 100*ms, pcm(0, 4800)), sample(100*ms, 100*ms, pcm(9000, 4800))}
	payloads := map[int64][]byte{0: pcm(0, 4800), 100 * ms: pcm(9000, 4800)}
	a := Analyze(2, samples, payloadMap(payloads), Options{Window: 100 * ms})
	b := Analyze(2, samples, payloadMap(payloads), Options{Window: 100 * ms})
	if a.Stats != b.Stats || a.SilentMs() != b.SilentMs() {
		t.Fatal("analysis not deterministic")
	}
}
