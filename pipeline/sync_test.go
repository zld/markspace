package pipeline

import (
	"testing"

	"github.com/zld/mediapipe/index"
	"github.com/zld/mediapipe/media"
)

func samplesAt(starts ...int64) []media.Sample {
	out := make([]media.Sample, len(starts))
	for i, s := range starts {
		dur := int64(33_333)
		if i+1 < len(starts) {
			dur = starts[i+1] - s
		}
		out[i] = media.Sample{Number: i + 1, StartUS: s, DurUS: dur}
	}
	return out
}

func TestSyncWithinTolerance(t *testing.T) {
	video := &index.TrackIndex{Samples: samplesAt(0, 1_000_000, 2_000_000, 3_000_000)}
	audio := &index.TrackIndex{Samples: samplesAt(0, 1_000_000, 2_000_000, 3_000_000)}
	rep := CheckSync(video, audio)
	if !rep.OK {
		t.Fatalf("expected OK: %+v", rep)
	}
	if rep.MaxErrorUS != 0 {
		t.Fatalf("error=%d", rep.MaxErrorUS)
	}
	if rep.ToleranceUS != 40_000 {
		t.Fatalf("tolerance=%d", rep.ToleranceUS)
	}
}

func TestSyncDetectsStartOffset(t *testing.T) {
	video := &index.TrackIndex{Samples: samplesAt(0, 1_000_000)}
	audio := &index.TrackIndex{Samples: samplesAt(100_000, 1_100_000)}
	rep := CheckSync(video, audio)
	if rep.StartErrorUS != -100_000 {
		t.Fatalf("start error=%d", rep.StartErrorUS)
	}
	if rep.OK {
		t.Fatal("100ms offset should fail 40ms tolerance")
	}
	if rep.Issues[0].Kind != media.KindSyncTolerance {
		t.Fatalf("issue kind=%v", rep.Issues[0].Kind)
	}
}

func TestSyncDetectsIntermediateDrift(t *testing.T) {
	// Audio starts and ends on time but drifts by 50ms in the middle.
	video := &index.TrackIndex{Samples: samplesAt(0, 1_000_000, 2_000_000)}
	audio := &index.TrackIndex{Samples: samplesAt(0, 1_050_000, 2_000_000)}
	rep := CheckSync(video, audio)
	if rep.MaxErrorUS < 50_000 {
		t.Fatalf("intermediate drift not detected: %d", rep.MaxErrorUS)
	}
	if rep.OK {
		t.Fatal("50ms intermediate drift should fail")
	}
	if rep.IntermediateNodes != 3 {
		t.Fatalf("intermediate nodes=%d", rep.IntermediateNodes)
	}
}

func TestSyncExactlyAtTolerancePasses(t *testing.T) {
	video := &index.TrackIndex{Samples: samplesAt(0)}
	audio := &index.TrackIndex{Samples: samplesAt(40_000)}
	rep := CheckSync(video, audio)
	if !rep.OK || rep.MaxErrorUS != 40_000 {
		t.Fatalf("at-tolerance should pass: ok=%v max=%d", rep.OK, rep.MaxErrorUS)
	}
}

func TestSyncSingleTrackPasses(t *testing.T) {
	video := &index.TrackIndex{Samples: samplesAt(0, 1_000_000)}
	if rep := CheckSync(video, nil); !rep.OK {
		t.Fatal("single track should pass with zero error")
	}
	if rep := CheckSync(nil, nil); !rep.OK {
		t.Fatal("empty input should pass with zero error")
	}
}
