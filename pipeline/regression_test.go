package pipeline

import (
	"bytes"
	"testing"

	"mediacore/index"
	"mediacore/media"
)

func TestSegmentsSplitOnTimestampRegression(t *testing.T) {
	frames := []*media.Frame{
		{Seq: 0, RTPTimestamp: 3600 * 2, Width: 8, Height: 8, DurationTicks: 3600, Payload: make([]byte, 64)},
		{Seq: 1, RTPTimestamp: 3600 * 3, Width: 8, Height: 8, DurationTicks: 3600, Payload: make([]byte, 64)},
		// 80 ms backward: a real regression beyond the 10 ms tolerance.
		{Seq: 2, RTPTimestamp: 3600 * 1, Width: 8, Height: 8, DurationTicks: 3600, Payload: make([]byte, 64)},
		{Seq: 3, RTPTimestamp: 3600 * 4, Width: 8, Height: 8, DurationTicks: 3600, Payload: make([]byte, 64)},
	}
	data, err := media.EncodeStream(frames)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.Scan(bytes.NewReader(data), index.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	seg, _ := NewSegmenter(DefaultSegmentConfig(media.DefaultTimescale))
	segments, _ := seg.Segments(idx.Entries)
	found := false
	for _, s := range segments {
		if s.ClosedBy == "regression" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a regression-closed segment, got %+v", segments)
	}
}

func TestSilenceNeighborWindowBlocksSingleQuietFrame(t *testing.T) {
	// One quiet frame surrounded by loud frames must not confirm silence, even
	// though its duration alone could exceed the minimum.
	cfg := DefaultSilenceConfig(media.DefaultTimescale)
	cfg.MinDurationTicks = 1 // make duration trivially satisfied
	det, _ := NewDetector(cfg)
	entries := []index.Entry{
		{Ticks: 0, DurationTicks: 3600, MetaStatus: string(media.MetaPresent), VolumeDBFS: volume(-5)},
		{Ticks: 3600, DurationTicks: 3600, MetaStatus: string(media.MetaPresent), VolumeDBFS: volume(-90)},
		{Ticks: 7200, DurationTicks: 3600, MetaStatus: string(media.MetaPresent), VolumeDBFS: volume(-5)},
	}
	got := Confirmed(det.Detect(entries))
	if len(got) != 0 {
		t.Fatalf("a single quiet frame must not confirm silence: %+v", got)
	}
}
