package pipeline_test

import (
	"errors"
	"testing"

	"github.com/zld/mediacore/internal/audio"
	"github.com/zld/mediacore/internal/pipeline"
	"github.com/zld/mediacore/internal/samplegen"
	"github.com/zld/mediacore/internal/segment"
	"github.com/zld/mediacore/internal/store"
	"github.com/zld/mediacore/internal/trace"
)

func opts() pipeline.Options {
	return pipeline.Options{
		Segment: segment.Params{
			Cuts: []segment.Cut{{At: 400_000_000}, {At: 600_000_000}},
			Transitions: []segment.Transition{{
				ID: "x", Start: 400_000_000, End: 600_000_000, FromTrack: 1, ToTrack: 2,
			}},
		},
		Silence: audio.DefaultConfig(),
	}
}

func TestProcessPropagatesRecoveryDamageToCutStage(t *testing.T) {
	g, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		t.Fatal(err)
	}
	offset, ok := samplegen.TruncateInSample(g, 1, 5, 16)
	if !ok {
		t.Fatal("inject truncation")
	}
	o := opts()
	o.Segment.Cuts = append(o.Segment.Cuts, segment.Cut{At: 500_000_000})

	res, err := pipeline.Process(store.SourceFromBytes(g.Data, "boundary.mp4"), o)
	if res == nil {
		t.Fatal("partial result required")
	}
	if len(res.Segments) != 2 {
		t.Fatalf("segments = %d", len(res.Segments))
	}
	var te *trace.Error
	if !errors.As(err, &te) || te.Code != trace.CodeCutPointCorrupt || te.Stage != trace.StageCut {
		t.Fatalf("err = %v", err)
	}
	if te.ByteOffset != offset || te.TimeNS != 500_000_000 || te.TrackID != 1 {
		t.Fatalf("location = %+v want offset %d", te, offset)
	}
	if !trace.HasCode(err, trace.CodeTruncated) {
		t.Fatalf("underlying recovery code missing: %v", err)
	}
}

func TestProcessPropagatesPayloadValidationToFingerprintStage(t *testing.T) {
	g, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		t.Fatal(err)
	}
	offset, ok := samplegen.FlipFirstPayloadByte(g, 1, 4)
	if !ok {
		t.Fatal("inject flip")
	}
	o := opts()
	o.VerifyPayloads = true

	res, err := pipeline.Process(store.SourceFromBytes(g.Data, "flip.mp4"), o)
	if res == nil || len(res.Segments) != 3 {
		t.Fatalf("result/segments = %+v", res)
	}
	var te *trace.Error
	if !errors.As(err, &te) || te.Code != trace.CodePayloadMismatch || te.Stage != trace.StageFingerprint {
		t.Fatalf("err = %v", err)
	}
	if !te.HasByteOffset || te.ByteOffset != offset || te.TrackID != 1 {
		t.Fatalf("fingerprint location = %+v want offset %d", te, offset)
	}
}
