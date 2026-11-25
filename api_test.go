package mediacore_test

import (
	"testing"

	mediacore "github.com/zld/mediacore"
	"github.com/zld/mediacore/internal/samplegen"
)

func TestPublicProcessBytesAPI(t *testing.T) {
	g, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		t.Fatal(err)
	}
	opts := mediacore.Options{
		Segment: mediacore.SegmentParams{
			Cuts: []mediacore.Cut{{At: 400_000_000}, {At: 600_000_000}},
			Transitions: []mediacore.Transition{{
				ID: "xfade", Start: 400_000_000, End: 600_000_000, FromTrack: 1, ToTrack: 2,
			}},
		},
		Silence: mediacore.DefaultSilenceConfig(),
	}
	res, err := mediacore.ProcessBytes(g.Data, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 3 || res.SourceDigest == "" || res.DerivedDigest == "" {
		t.Fatalf("unexpected public result: %+v", res)
	}
	if len(res.Duplicates) != 1 {
		t.Fatalf("duplicates = %+v", res.Duplicates)
	}
}

func TestPublicErrorAliases(t *testing.T) {
	g, _ := samplegen.Generate(samplegen.NormalSpec(true))
	if _, ok := samplegen.TruncateInSample(g, 1, 5, 16); !ok {
		t.Fatal("inject")
	}
	opts := mediacore.Options{
		Segment: mediacore.SegmentParams{Cuts: []mediacore.Cut{{At: 500_000_000}}},
	}
	_, err := mediacore.ProcessBytes(g.Data, opts)
	if !mediacore.HasCode(err, mediacore.CodeCutPointCorrupt) {
		t.Fatalf("error = %v", err)
	}
	te, ok := mediacore.AsError(err)
	if !ok || te.Stage != mediacore.StageCut || te.TrackID != 1 {
		t.Fatalf("located error = %#v", te)
	}
}
