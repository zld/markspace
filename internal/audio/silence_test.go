package audio

import (
	"encoding/binary"
	"testing"

	"github.com/zld/mediacore/internal/ingest"
	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/samplegen"
)

func pcmBytes(values []int16) []byte {
	b := make([]byte, len(values)*2)
	for i, v := range values {
		binary.LittleEndian.PutUint16(b[i*2:i*2+2], uint16(v))
	}
	return b
}

func TestDetectSilenceThresholdAndMinimumDuration(t *testing.T) {
	values := make([]int16, 80)
	for i := range values {
		values[i] = 1000
	}
	for i := 10; i < 60; i++ {
		values[i] = 0
	}
	tr := &model.Track{ID: 2, TimeScale: 1000, Samples: []*model.Sample{{
		StartTick: 0, DurationTick: 80, Data: pcmBytes(values), Complete: true,
	}}}
	got, err := DetectTrack(tr, Config{ThresholdAmplitude: 50, MinDurationNS: 40_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("silences = %+v", got)
	}
	if got[0].Start != 10_000_000 || got[0].End != 60_000_000 {
		t.Fatalf("range = [%d,%d)", got[0].Start, got[0].End)
	}

	got, _ = DetectTrack(tr, Config{ThresholdAmplitude: 50, MinDurationNS: 60_000_000})
	if len(got) != 0 {
		t.Fatalf("50ms silence must be filtered by 60ms minimum: %+v", got)
	}
}

func TestDetectGeneratedFixtureSilenceOnCommonBase(t *testing.T) {
	g, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ingest.ParseMP4(g.Data)
	if err != nil {
		t.Fatal(err)
	}
	tl, err := model.Normalize("d", doc.Tracks, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DetectTrack(tl.Track(3), DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("silences = %+v", got)
	}
	if got[0].Start != 300_000_000 || got[0].End != 500_000_000 {
		t.Fatalf("generated silence = [%d,%d), want [300ms,500ms)", got[0].Start, got[0].End)
	}
}
