package manifest

import (
	"bytes"
	"encoding/json"
	"testing"

	"mediacore/index"
	"mediacore/media"
	"mediacore/pipeline"
)

func vol(v float64) *float64 { return &v }

// TestPerSegmentSilenceSumsToTotal checks that a quiet interval crossing a
// segment boundary is attributed to segments without double counting, using
// the production Build path.
func TestPerSegmentSilenceSumsToTotal(t *testing.T) {
	frames := make([]*media.Frame, 300)
	for i := 0; i < 300; i++ {
		f := &media.Frame{
			Seq: uint32(i), RTPTimestamp: uint32(i * 3600), CaptureTimeMS: int64(i) * 40,
			Width: 8, Height: 8, DurationTicks: 3600, StreamID: 1, Payload: make([]byte, 64),
		}
		db := -6.0
		if i >= 90 && i < 150 { // 90..150 crosses the 4 s boundary
			db = -70
		}
		b, _ := json.Marshal(media.Meta{VolumeDBFS: vol(db)})
		f.MetaRaw = b
		frames[i] = f
	}
	data, err := media.EncodeStream(frames)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.Scan(bytes.NewReader(data), index.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	res, err := pipeline.Run(idx, pipeline.NewBufferSource(data, media.DefaultLimits()),
		pipeline.DefaultConfig(media.DefaultTimescale))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Build(idx, res, Options{TargetKBPS: 900})
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	for _, s := range m.Segments {
		sum += s.SilenceMS
	}
	if sum != m.Stats.SilenceConfirmedMS {
		t.Fatalf("per-segment silence=%dms total=%dms", sum, m.Stats.SilenceConfirmedMS)
	}
	if sum == 0 {
		t.Fatal("expected non-zero silence to make this meaningful")
	}
}
