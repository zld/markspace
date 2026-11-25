package media

import (
	"testing"

	"archivecheck/internal/diagnostic"
)

func audioSource() TrackDescription {
	return TrackDescription{
		Index: 3, ID: 1, Kind: "audio", Codec: "mp4a", Timescale: 48000,
		DurationNS: 2_000_000_000, Channels: 6, ChannelLayout: Layout51,
		SampleRate: 48000, MetadataOffset: 128,
	}
}

func TestLayoutPreservesMatchingAndUnrequestedMetadata(t *testing.T) {
	plan, err := ResolveLayout(3, audioSource(), RequestedLayout{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Accepted || len(plan.Decisions) != 3 {
		t.Fatalf("plan = %+v", plan)
	}
	for _, d := range plan.Decisions {
		if d.Action != ActionPreserve {
			t.Fatalf("decision %s action = %s, want preserve", d.Field, d.Action)
		}
	}
}

func TestLayoutChannelConflictRequiresExplicitMap(t *testing.T) {
	req := RequestedLayout{Channels: 2, ChannelLayout: LayoutStereo}
	plan, err := ResolveLayout(3, audioSource(), req)
	if !diagnostic.HasCode(err, diagnostic.CodeMetadataConflict) {
		t.Fatalf("err = %v, want METADATA_CONFLICT", err)
	}
	if plan.Accepted {
		t.Fatal("plan must not be accepted after reject")
	}
	if plan.Decisions[0].Action != ActionReject {
		t.Fatalf("channel action = %s, want reject", plan.Decisions[0].Action)
	}
	de, _ := diagnostic.From(err)
	if de.Sample != 3 || de.Offset != 128 {
		t.Fatalf("location = sample%d offset%d", de.Sample, de.Offset)
	}

	req.ChannelMappingID = "archive-5.1-to-stereo-v1"
	plan, err = ResolveLayout(3, audioSource(), req)
	if err != nil {
		t.Fatalf("declared mapping should be accepted: %v", err)
	}
	if plan.Decisions[0].Action != ActionRemap || plan.Decisions[0].Reason == "" {
		t.Fatalf("channel decision = %+v", plan.Decisions[0])
	}
}

func TestLayoutSampleRateRequiresDeclaredResampler(t *testing.T) {
	req := RequestedLayout{SampleRate: 44100}
	if _, err := ResolveLayout(4, audioSource(), req); !diagnostic.HasCode(err, diagnostic.CodeMetadataConflict) {
		t.Fatalf("unmapped resample err = %v", err)
	}
	req.ResampleAlgorithm = "rational-ns-v1"
	plan, err := ResolveLayout(4, audioSource(), req)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Decisions[1].Action != ActionRemap {
		t.Fatalf("sample decision = %+v", plan.Decisions[1])
	}
}

func TestLayoutColorRequiresExplicitRule(t *testing.T) {
	source := audioSource()
	source.Kind = "video"
	source.Color = &ColorMetadata{ColourType: "nclx", Primaries: 1, Transfer: 1, Matrix: 1}
	target := &ColorMetadata{ColourType: "nclx", Primaries: 9, Transfer: 16, Matrix: 9}
	req := RequestedLayout{Color: target}
	if _, err := ResolveLayout(5, source, req); !diagnostic.HasCode(err, diagnostic.CodeMetadataConflict) {
		t.Fatalf("color err = %v", err)
	}
	req.ColorConversionID = "bt709-to-bt2020-v1"
	plan, err := ResolveLayout(5, source, req)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Decisions[2].Action != ActionRemap {
		t.Fatalf("color decision = %+v", plan.Decisions[2])
	}
}

func TestValidateDescriptionRejectsInconsistentLayout(t *testing.T) {
	bad := audioSource()
	bad.Channels = 2
	err := ValidateDescription(6, bad)
	if !diagnostic.HasCode(err, diagnostic.CodeMetadataConflict) {
		t.Fatalf("err = %v", err)
	}
}
