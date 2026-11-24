package syncer

import (
	"errors"
	"testing"
	"time"

	"mediarc/internal/model"
)

func testConfig(offset time.Duration) Config {
	tb := model.Timebase{Timescale: model.VideoTimescale}
	return Config{
		FrameDelta: model.VideoFrameDelta,
		Mapping: model.ClockMap{
			Name:         "rtp48k-to-media90k",
			SourceRate:   model.AudioClockRate,
			Media:        tb,
			OriginSource: 0,
			OriginMedia:  0,
			StartOffset:  offset,
		},
	}
}

func TestAlignWithinOneFrame(t *testing.T) {
	// 480 samples at 48k = 10 ms => 900 ticks at 90k.
	got, err := Align(480, 900, testConfig(0), &model.Collector{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.MappedPTS != 900 || got.AlignedPTS != 0 {
		t.Fatalf("alignment=%+v", got)
	}
}

func TestAlignBeyondFramePreservesOriginalsAndExit3(t *testing.T) {
	faults := &model.Collector{}
	cfg := testConfig(50 * time.Millisecond) // 4500 ticks > 3600 frame
	got, err := Align(0, 0, cfg, faults)
	var ee *model.ExitError
	if !errors.As(err, &ee) || ee.Code != model.ExitSyncConfig {
		t.Fatalf("err=%v want exit 3", err)
	}
	if got.Sync.OriginalAudio != 4500 || got.Sync.OriginalVideo != 0 {
		t.Fatalf("original times not preserved: %+v", got.Sync)
	}
	if got.OK {
		t.Fatal("failed sync marked OK")
	}
	if !faults.Has(model.FaultSync) {
		t.Fatal("sync fault not recorded")
	}
}
