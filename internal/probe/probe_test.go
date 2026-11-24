package probe

import (
	"testing"

	"archive/internal/essence"
	"archive/internal/fault"
	"archive/internal/fixture"
	"archive/internal/model"
)

func buildSpec(t *testing.T, name string, damage []fixture.DamageKind) (fixture.Spec, *fixture.Layout, []byte) {
	t.Helper()
	spec, ok := fixture.FindSpec(fixture.DefaultMixed(), name)
	if !ok {
		t.Fatalf("spec %s missing", name)
	}
	layout, data, err := fixture.BuildMP4(spec)
	if spec.Format == model.ContainerMXF {
		layout, data, err = fixture.BuildMXF(spec)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(damage) > 0 {
		lean := spec
		lean.Tracks = append([]fixture.TrackSpec(nil), spec.Tracks...)
		if contains(damage, fixture.DamageIndex) {
			for i := range lean.Tracks {
				lean.Tracks[i].DropsIndex = true
			}
			layout, data, err = rebuild(lean, spec.Format)
			if err != nil {
				t.Fatal(err)
			}
		}
		var kinds []fixture.DamageKind
		for _, k := range damage {
			if k != fixture.DamageIndex {
				kinds = append(kinds, k)
			}
		}
		plan, _, err := fixture.Plan(layout, kinds)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan) > 0 {
			data, _, err = fixture.Apply(layout, data, plan)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return spec, layout, data
}

func rebuild(s fixture.Spec, format model.ContainerKind) (*fixture.Layout, []byte, error) {
	if format == model.ContainerMP4 {
		return fixture.BuildMP4(s)
	}
	return fixture.BuildMXF(s)
}

func contains(kinds []fixture.DamageKind, k fixture.DamageKind) bool {
	for _, x := range kinds {
		if x == k {
			return true
		}
	}
	return false
}

func TestProbeMP4Complete(t *testing.T) {
	spec, layout, data := buildSpec(t, "clip_b", nil)
	f, err := Probe(Source{Name: "clip_b", Data: data}, model.ContainerMP4)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Tracks) != len(spec.Tracks) {
		t.Fatalf("tracks = %d", len(f.Tracks))
	}
	for _, tl := range layout.Tracks {
		var got *model.Track
		for _, tr := range f.Tracks {
			if tr.Logical == string(tl.Spec.Kind) {
				got = tr
			}
		}
		if got == nil {
			t.Fatalf("logical %s missing", tl.Spec.Kind)
		}
		if !got.Index.Present || !got.Index.Complete {
			t.Fatalf("%s index incomplete: %+v", got.Key, got.Index)
		}
		if len(got.Fragments) != tl.Spec.Frames {
			t.Fatalf("%s fragments = %d, want %d", got.Key, len(got.Fragments), tl.Spec.Frames)
		}
		for i, fr := range got.Fragments {
			want := tl.Frames[i]
			if fr.PayloadOffset != want.Offset || fr.Size != want.Size {
				t.Fatalf("%s fragment %d = %+v, want off %d size %d", got.Key, i, fr, want.Offset, want.Size)
			}
			if _, err := essence.ParseHeader(data[fr.PayloadOffset : fr.PayloadOffset+fr.Size]); err != nil {
				t.Fatalf("%s frame %d unreadable: %v", got.Key, i, err)
			}
		}
	}
}

func TestProbeMXFComplete(t *testing.T) {
	spec, layout, data := buildSpec(t, "reel_a", nil)
	f, err := Probe(Source{Name: "reel_a", Data: data}, model.ContainerMXF)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Tracks) != len(spec.Tracks) || f.Partitions < 2 {
		t.Fatalf("file = %+v", f)
	}
	for _, tl := range layout.Tracks {
		var got *model.Track
		for _, tr := range f.Tracks {
			if tr.TrackID == tl.Spec.TrackID {
				got = tr
			}
		}
		if got == nil {
			t.Fatalf("track %d missing", tl.Spec.TrackID)
		}
		if !got.Index.Complete {
			t.Fatalf("track %d index: %+v faults %v", tl.Spec.TrackID, got.Index, DescribeFaults(f.Faults))
		}
		if len(got.Fragments) != tl.Spec.Frames {
			t.Fatalf("track %d fragments = %d", tl.Spec.TrackID, len(got.Fragments))
		}
		if got.RotationDeg != tl.Spec.Rotation {
			t.Fatalf("track %d rotation = %d, want %d", tl.Spec.TrackID, got.RotationDeg, tl.Spec.Rotation)
		}
		for i, fr := range got.Fragments {
			want := tl.Frames[i]
			if fr.PayloadOffset != want.Offset+20 || fr.Size != want.Size {
				t.Fatalf("track %d fragment %d payload = %d/%d, want %d/%d", tl.Spec.TrackID, i, fr.PayloadOffset, fr.Size, want.Offset+20, want.Size)
			}
		}
		if tl.Spec.Crop != nil {
			if got.Crop == nil || *got.Crop != *tl.Spec.Crop {
				t.Fatalf("track %d crop = %+v, want %+v", tl.Spec.TrackID, got.Crop, tl.Spec.Crop)
			}
		}
		if tl.Spec.Kind == model.KindVideo && got.Color.Primaries.Value != tl.Spec.Color.Primaries {
			t.Fatalf("track %d primaries = %s, want %s", tl.Spec.TrackID, got.Color.Primaries.Value, tl.Spec.Color.Primaries)
		}
		if tl.Spec.Kind == model.KindVideo && got.Color.Primaries.Source != "mxf" {
			t.Fatalf("video colour source = %s", got.Color.Primaries.Source)
		}
	}
}

func TestProbeMP4RotationAndColour(t *testing.T) {
	_, _, data := buildSpec(t, "clip_b", nil)
	f, err := Probe(Source{Name: "clip_b", Data: data}, model.ContainerMP4)
	if err != nil {
		t.Fatal(err)
	}
	var video *model.Track
	for _, tr := range f.Tracks {
		if tr.Kind == model.KindVideo {
			video = tr
		}
	}
	if video == nil {
		t.Fatal("no video track")
	}
	if video.RotationDeg != 270 {
		t.Fatalf("rotation = %d", video.RotationDeg)
	}
	if video.Color.Primaries.Value != "bt2020" || video.Color.Transfer.Value != "smpte2084" || video.Color.Range.Value != "full" {
		t.Fatalf("colour = %+v", video.Color)
	}
	if video.Crop == nil || video.Crop.Right != 1080 || video.Crop.Bottom != 1920 {
		t.Fatalf("clip_b crop = %+v", video.Crop)
	}
}

func TestProbeMXFMissingIndexIsIncomplete(t *testing.T) {
	_, _, data := buildSpec(t, "reel_a", []fixture.DamageKind{fixture.DamageIndex})
	f, err := Probe(Source{Name: "reel_a", Data: data}, model.ContainerMXF)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range f.Tracks {
		if tr.Index.Complete {
			t.Fatalf("%s index claimed complete without a segment", tr.Key)
		}
		if tr.Index.Present {
			t.Fatalf("%s index claimed present", tr.Key)
		}
	}
	if f.Faults.ByCode(fault.MissingIndex) == 0 {
		t.Fatalf("no missing_index fault: %v", DescribeFaults(f.Faults))
	}
	for _, e := range f.Faults {
		if e.Code == fault.MissingIndex && e.Track == "" {
			t.Fatalf("missing index fault has no track: %v", e.Line())
		}
	}
}

func TestProbeMP4MissingSampleTableIsIncomplete(t *testing.T) {
	_, _, data := buildSpec(t, "clip_b", []fixture.DamageKind{fixture.DamageIndex})
	f, err := Probe(Source{Name: "clip_b", Data: data}, model.ContainerMP4)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range f.Tracks {
		if tr.Index.Complete {
			t.Fatalf("%s index claimed complete without stbl entries", tr.Key)
		}
	}
	if f.Faults.ByCode(fault.MissingIndex) == 0 {
		t.Fatalf("no missing_index fault")
	}
}

func TestProbeTruncatedMXFReportsOffset(t *testing.T) {
	_, layout, data := buildSpec(t, "reel_a", nil)
	// Cut the file inside the last frame.
	last := uint64(0)
	for _, tl := range layout.Tracks {
		for _, fr := range tl.Frames {
			if fr.Offset+fr.Size > last {
				last = fr.Offset + fr.Size
			}
		}
	}
	cut := data[:last-5]
	f, err := Probe(Source{Name: "reel_a", Data: cut}, model.ContainerMXF)
	if err != nil {
		t.Fatal(err)
	}
	if f.Faults.ByCode(fault.TruncatedBox) == 0 {
		t.Fatalf("truncation not reported: %v", DescribeFaults(f.Faults))
	}
	// Every reported truncated box must have a real offset inside the file.
	for _, e := range f.Faults {
		if e.Code == fault.TruncatedBox && e.Offset >= uint64(len(cut)) {
			t.Fatalf("fault offset %d outside file of %d bytes", e.Offset, len(cut))
		}
	}
}

func TestProbeMP4TruncatedBox(t *testing.T) {
	_, _, data := buildSpec(t, "clip_b", nil)
	// Cut the file in the middle of the mdat so the payload is incomplete
	// while the header boxes survive.
	cut := data[:len(data)/2]
	f, err := Probe(Source{Name: "clip_b", Data: cut}, model.ContainerMP4)
	if err != nil {
		t.Fatal(err)
	}
	if f.Faults.ByCode(fault.TruncatedBox) == 0 {
		t.Fatalf("no truncated box fault: %v", DescribeFaults(f.Faults))
	}
}

func TestProbeMP4InvalidLength(t *testing.T) {
	_, _, data := buildSpec(t, "clip_b", nil)
	// Corrupt the first box size so it runs past the end of the file.
	bad := append([]byte(nil), data...)
	bad[0] = 0x7F
	bad[1] = 0xFF
	f, err := Probe(Source{Name: "clip_b", Data: bad}, model.ContainerMP4)
	if err == nil {
		if f.Faults.ByCode(fault.TruncatedBox) == 0 && f.Faults.ByCode(fault.InvalidLength) == 0 {
			t.Fatalf("oversized first box not reported: %v", DescribeFaults(f.Faults))
		}
	}
}

func TestAssignLogicalDisambiguates(t *testing.T) {
	tracks := []*model.Track{
		{Kind: model.KindVideo, TrackID: 1},
		{Kind: model.KindVideo, TrackID: 2},
		{Kind: model.KindAudio, TrackID: 3},
	}
	assignLogical(tracks)
	if tracks[0].Logical != "video#1" || tracks[1].Logical != "video#2" || tracks[2].Logical != "audio" {
		t.Fatalf("logical = %q %q %q", tracks[0].Logical, tracks[1].Logical, tracks[2].Logical)
	}
}

func TestRotationFromMatrix(t *testing.T) {
	cases := []struct {
		m    [9]int32
		want int
	}{
		{[9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}, 0},
		{[9]int32{0, 0x10000, 0, -0x10000, 0, 0, 0, 0, 0x40000000}, 90},
		{[9]int32{-0x10000, 0, 0, 0, -0x10000, 0, 0, 0, 0x40000000}, 180},
		{[9]int32{0, -0x10000, 0, 0x10000, 0, 0, 0, 0, 0x40000000}, 270},
	}
	for _, c := range cases {
		if got := rotationFromMatrix(c.m); got != c.want {
			t.Fatalf("matrix %v -> %d, want %d", c.m, got, c.want)
		}
	}
}

func TestDigestIsStable(t *testing.T) {
	a := Source{Name: "a", Data: []byte("hello")}
	b := Source{Name: "b", Data: []byte("hello")}
	if a.Digest() != b.Digest() {
		t.Fatal("digest must depend only on bytes")
	}
	c := Source{Name: "a", Data: []byte("hello!")}
	if a.Digest() == c.Digest() {
		t.Fatal("different bytes must differ")
	}
	if len(a.Digest()) != 64 {
		t.Fatalf("digest length = %d", len(a.Digest()))
	}
}
