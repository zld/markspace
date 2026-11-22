package segment

import (
	"bytes"
	"encoding/json"
	"testing"

	"mediapipeline/internal/container"
	"mediapipeline/internal/fixture"
	"mediapipeline/internal/media"
)

func ms(v int64) media.Nano { return media.Nano(v) * media.Nano(1_000_000) }

// makeTrack builds a parsed video track directly (no file I/O) for alignment
// tests. Keyframe positions are given in milliseconds.
func makeTrack(t *testing.T, id uint32, timescale uint32, sampleMS []int64, keyframeMS map[int64]bool) *container.Track {
	t.Helper()
	tr := &container.Track{ID: id, Kind: "video", Timescale: timescale}
	for i, tms := range sampleMS {
		tr.Samples = append(tr.Samples, container.Sample{
			Index:    i,
			DTS:      tms,
			PTS:      tms,
			DTSNanos: ms(tms),
			PTSNanos: ms(tms),
			Keyframe: keyframeMS[tms],
		})
		if keyframeMS[tms] {
			tr.Keyframes = append(tr.Keyframes, i)
		}
	}
	if len(sampleMS) > 0 {
		tr.Samples[len(tr.Samples)-1].DeltaNanos = ms(20)
	}
	return tr
}

// regularTrack creates a sample every 100ms for durationMS, keyframes at the
// given positions.
func regularTrack(t *testing.T, id uint32, durationMS int64, keyframeEveryMS int64) *container.Track {
	var sampleMS []int64
	keys := map[int64]bool{}
	for t0 := int64(0); t0 < durationMS; t0 += 100 {
		sampleMS = append(sampleMS, t0)
		if t0%keyframeEveryMS == 0 {
			keys[t0] = true
		}
	}
	return makeTrack(t, id, 1000, sampleMS, keys)
}

func TestAlignSharedBoundaries(t *testing.T) {
	targets := []Target{
		{Name: "400", Video: regularTrack(t, 1, 12000, 1000)},
		{Name: "900", Video: regularTrack(t, 2, 12000, 2000)},
		{Name: "1600", Video: regularTrack(t, 3, 12000, 2000)},
	}
	plan, err := Align(targets, Config{TargetDuration: ms(2000), MaxSegments: 100})
	if err != nil {
		t.Fatalf("Align: %v", err)
	}
	if len(plan.Boundaries) == 0 {
		t.Fatal("no boundaries")
	}
	for i := 1; i < len(plan.Boundaries); i++ {
		if plan.Boundaries[i].Start <= plan.Boundaries[i-1].Start {
			t.Fatalf("boundaries not ascending at %d", i)
		}
	}
	// The first segment starts at 0; shared cuts follow every 2s.
	if plan.Boundaries[0].Start != 0 {
		t.Errorf("first boundary = %d ms, want 0", int64(plan.Boundaries[0].Start)/1e6)
	}
	if plan.Boundaries[1].Start != ms(2000) {
		t.Errorf("second boundary = %d ms, want 2000", int64(plan.Boundaries[1].Start)/1e6)
	}
	// All targets must have the same segment count.
	want := len(plan.Boundaries)
	for _, tp := range plan.PerTarget {
		if tp.SegmentCount != want {
			t.Errorf("%s has %d segments, want %d", tp.Name, tp.SegmentCount, want)
		}
	}
	// Every representation's slot list must be identical in length.
	for _, tp := range plan.PerTarget {
		if len(tp.VideoStarts) != len(plan.Boundaries) {
			t.Errorf("%s has %d starts for %d boundaries", tp.Name, len(tp.VideoStarts), len(plan.Boundaries))
		}
	}
}

func TestAlignRejectsWhenNoSharedBoundary(t *testing.T) {
	// Keyframes at 2s and 3s multiples never coincide beyond zero.
	targets := []Target{
		{Name: "a", Video: regularTrack(t, 1, 12000, 2000)},
		{Name: "b", Video: regularTrack(t, 2, 12000, 3000)},
	}
	_, err := Align(targets, Config{TargetDuration: ms(2000), MaxSegments: 100, MaxStretchFactor: 1})
	if err == nil {
		t.Fatal("expected missing shared boundary error")
	}
	if media.CategoryOf(err) != media.BoundaryNotFound {
		t.Fatalf("category = %v", media.CategoryOf(err))
	}
	if e := media.AsError(err); !e.HasOff && !e.HasTrack {
		t.Fatalf("error lacks location: %v", err)
	}
}

func TestAlignChoosesLatestKeyframeNotTarget(t *testing.T) {
	// Keyframes every 700ms: target 1000ms must snap back to 700ms.
	targets := []Target{{Name: "only", Video: regularTrack(t, 1, 10000, 700)}}
	plan, err := Align(targets, Config{TargetDuration: ms(1000), MaxSegments: 100})
	if err != nil {
		t.Fatalf("Align: %v", err)
	}
	if got := plan.Boundaries[0].Start; got != 0 {
		t.Errorf("first boundary = %d, want 0", int64(got)/1e6)
	}
	if got := plan.Boundaries[1].Start; got != ms(700) {
		t.Errorf("second boundary = %d, want 700", int64(got)/1e6)
	}
	if got := plan.Boundaries[2].Start; got != ms(1400) {
		t.Errorf("third boundary = %d, want 1400", int64(got)/1e6)
	}
}

func TestAlignFailsWithoutKeyframe(t *testing.T) {
	tr := regularTrack(t, 1, 5000, 1000)
	tr.Keyframes = nil // simulate corrupt/absent stss
	_, err := Align([]Target{{Name: "x", Video: tr}}, Config{TargetDuration: ms(1000), MaxSegments: 10})
	if err == nil {
		t.Fatal("expected boundary error")
	}
	if media.CategoryOf(err) != media.BoundaryNotFound {
		t.Fatalf("category = %v", media.CategoryOf(err))
	}
	if e := media.AsError(err); !e.HasTrack && !e.HasOff {
		t.Fatalf("error lacks location: %v", err)
	}
}

func TestAlignRejectsEmptySegment(t *testing.T) {
	// Only one keyframe at t=0 for a long timeline: no boundary can be placed.
	tr := makeTrack(t, 1, 1000, []int64{0, 100, 200, 300}, map[int64]bool{0: true})
	_, err := Align([]Target{{Name: "x", Video: tr}}, Config{TargetDuration: ms(100), MaxSegments: 10, MaxStretchFactor: 1})
	if err == nil {
		t.Fatal("expected empty-segment failure")
	}
	if media.CategoryOf(err) != media.BoundaryNotFound {
		t.Fatalf("category = %v", media.CategoryOf(err))
	}
}

func TestAlignWithAudioSnapsAudioStart(t *testing.T) {
	video := regularTrack(t, 1, 6000, 1000)
	audio := makeTrack(t, 2, 1000, []int64{0, 20, 40}, map[int64]bool{0: true, 20: true, 40: true})
	audio.Kind = "audio"
	for i := range audio.Samples {
		audio.Samples[i].Keyframe = true
	}
	plan, err := Align([]Target{{Name: "v", Video: video, Audio: audio}}, Config{TargetDuration: ms(1000), MaxSegments: 10})
	if err != nil {
		t.Fatalf("Align: %v", err)
	}
	for _, b := range plan.Boundaries {
		if !b.HasAudio {
			t.Fatalf("boundary %d missing audio", b.Start)
		}
		if b.AudioPTS > b.Start {
			t.Fatalf("audio PTS %d after video boundary %d", b.AudioPTS, b.Start)
		}
	}
}

func TestBuildManifestIsDeterministic(t *testing.T) {
	spec := fixture.MovieSpec{
		Timescale: 1000,
		Tracks:    []fixture.TrackSpec{videoTrackSpec(1, 30000, 120, 21, 30)},
	}
	data, err := fixture.BuildMovie(spec)
	if err != nil {
		t.Fatal(err)
	}
	movie, err := container.Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	tr := movie.Tracks[0]
	read := func(i int) ([]byte, error) {
		s := tr.Samples[i]
		end := s.Offset + uint64(s.Size)
		if end > uint64(len(data)) {
			t.Fatalf("sample %d beyond file", i)
		}
		return data[s.Offset:end], nil
	}
	plan, err := Align([]Target{{Name: "v", Video: tr}}, Config{TargetDuration: ms(2000), MaxSegments: 100})
	if err != nil {
		t.Fatalf("Align: %v", err)
	}
	inputs := []BuildInput{{Name: "v", Bitrate: 400, Track: tr, Read: read}}
	m1, err := Build(plan, inputs, ms(2000))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	json1, hash1, err := m1.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := m1.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// Rebuild from scratch: byte-identical JSON and hash.
	plan2, _ := Align([]Target{{Name: "v", Video: tr}}, Config{TargetDuration: ms(2000), MaxSegments: 100})
	m2, err := Build(plan2, inputs, ms(2000))
	if err != nil {
		t.Fatal(err)
	}
	json2, hash2, _ := m2.Seal()
	if hash1 != hash2 || !bytes.Equal(json1, json2) {
		t.Fatalf("manifest not deterministic: %s vs %s", hash1, hash2)
	}
	if len(hash1) != 64 {
		t.Fatalf("hash length = %d", len(hash1))
	}
}

func videoTrackSpec(id uint32, timescale uint32, count int, keyEvery int, seed uint64) fixture.TrackSpec {
	tr := fixture.TrackSpec{ID: id, Handler: "vide", Timescale: timescale, Width: 320, Height: 180, Seed: seed}
	for i := 0; i < count; i++ {
		tr.Samples = append(tr.Samples, fixture.Sample{
			Delta: uint32(timescale / 30),
			Size:  uint32(64 + i%5),
			Sync:  i%keyEvery == 0,
		})
	}
	return tr
}

func TestManifestValidateCatchesMismatchedBoundaries(t *testing.T) {
	m := &Manifest{
		Schema:            ManifestSchema,
		SegmentCount:      2,
		AlignedBoundaries: 2,
		Representations: []Representation{
			{Name: "a", Segments: []SegmentEntry{
				{Index: 0, StartNanos: 0, EndNanos: 100, SHA256: "x"},
				{Index: 1, StartNanos: 100, EndNanos: 200, SHA256: "y"},
			}},
			{Name: "b", Segments: []SegmentEntry{
				{Index: 0, StartNanos: 0, EndNanos: 90, SHA256: "x"},
				{Index: 1, StartNanos: 90, EndNanos: 200, SHA256: "y"},
			}},
		},
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected boundary mismatch")
	}
	if media.CategoryOf(err) != media.BoundaryNotFound {
		t.Fatalf("category = %v", media.CategoryOf(err))
	}
}

func TestManifestValidateRejectsDuplicateNames(t *testing.T) {
	m := &Manifest{
		Schema:            ManifestSchema,
		SegmentCount:      1,
		AlignedBoundaries: 1,
		Representations: []Representation{
			{Name: "dup", Segments: []SegmentEntry{{Index: 0, StartNanos: 0, EndNanos: 100, SHA256: "x"}}},
			{Name: "dup", Segments: []SegmentEntry{{Index: 0, StartNanos: 0, EndNanos: 100, SHA256: "x"}}},
		},
	}
	if err := m.Validate(); err == nil {
		t.Fatal("expected duplicate name failure")
	}
}

func TestManifestJSONRoundTrip(t *testing.T) {
	m := &Manifest{
		Schema:            ManifestSchema,
		ContentID:         "abc",
		SegmentCount:      1,
		AlignedBoundaries: 1,
		Representations: []Representation{{
			Name: "a", Bitrate: 400, TrackID: 1, Timescale: 1000, TotalBytes: 4,
			Segments: []SegmentEntry{{Index: 0, StartNanos: 0, EndNanos: 100, ByteSize: 4, SHA256: "x"}},
		}},
	}
	data, hash, err := m.Seal()
	if err != nil {
		t.Fatal(err)
	}
	var back Manifest
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	backHash, err := back.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if backHash != hash {
		t.Fatalf("hash after round trip = %s, want %s", backHash, hash)
	}
}
