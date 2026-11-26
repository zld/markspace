package adapter_test

import (
	"context"
	"errors"
	"testing"

	"assetpack/internal/adapter"
	"assetpack/internal/samplegen"
)

func TestRegistryRejectsUnknownVersionAndCodec(t *testing.T) {
	r := adapter.DefaultRegistry()
	if _, err := r.Format("mp4/v9"); !errors.Is(err, adapter.ErrUnknownVersion) {
		t.Fatalf("unknown mp4 version err=%v", err)
	}
	if _, err := r.Codec("vp9/v1"); !errors.Is(err, adapter.ErrUnknownCodec) {
		t.Fatalf("unknown codec err=%v", err)
	}
}

func TestMP4V1RoundTripAndTruncatedMDAT(t *testing.T) {
	built, err := samplegen.BuildContainer(1, false)
	if err != nil {
		t.Fatal(err)
	}
	a, err := adapter.DefaultRegistry().Format("mp4/v1")
	if err != nil {
		t.Fatal(err)
	}
	asset, err := a.Parse(context.Background(), adapter.Source{Name: "seq1.mp4", Data: built.Data})
	if err != nil {
		t.Fatalf("parse generated mp4: %v", err)
	}
	if asset.Duration != 180000 || len(asset.Tracks) != 2 || len(asset.Events) != 1 {
		t.Fatalf("duration=%d tracks=%d events=%d", asset.Duration, len(asset.Tracks), len(asset.Events))
	}
	if got := asset.Tracks[0].Samples[12].PTS; got != 45000 {
		t.Fatalf("frame 12 PTS=%d want45000", got)
	}

	bad := samplegen.TruncateMDAT(built.Data, 128)
	_, err = a.Parse(context.Background(), adapter.Source{Name: "bad-mdat.mp4", Data: bad})
	if err == nil {
		t.Fatal("truncated mdat must be rejected")
	}
	if !errors.Is(err, adapter.ErrBadInput) {
		t.Fatalf("error should wrap ErrBadInput: %v", err)
	}
}

func TestFMP4V1RequiresFragmentAndParses(t *testing.T) {
	built, err := samplegen.BuildContainer(2, true)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := adapter.DefaultRegistry().Format("fmp4/v1")
	asset, err := a.Parse(context.Background(), adapter.Source{Data: built.Data})
	if err != nil {
		t.Fatal(err)
	}
	if asset.Descriptor.ID.String() != "fmp4/v1" || len(asset.Tracks) != 2 {
		t.Fatalf("bad fmp4 asset %+v", asset.Descriptor)
	}
}

func TestRTPV1SequenceFaults(t *testing.T) {
	a, _ := adapter.DefaultRegistry().Format("rtp/v1")
	for _, fault := range []string{"", "rtp-duplicate", "rtp-missing"} {
		data, err := samplegen.BuildRTPStream(fault)
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.Parse(context.Background(), adapter.Source{Data: data})
		if fault == "" && err != nil {
			t.Fatalf("valid RTP: %v", err)
		}
		if fault != "" && err == nil {
			t.Fatalf("%s must fail", fault)
		}
	}
}

func TestUnknownMP4ManifestVersion(t *testing.T) {
	data, err := samplegen.UnknownVersionContainer()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := adapter.DefaultRegistry().Format("mp4/v1")
	_, err = a.Parse(context.Background(), adapter.Source{Data: data})
	if !errors.Is(err, adapter.ErrUnknownVersion) {
		t.Fatalf("want unknown version, got %v", err)
	}
}
