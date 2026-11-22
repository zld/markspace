package adapter

import (
	"testing"

	"example.com/mediaforge/internal/fixture"
	"example.com/mediaforge/internal/merrors"
)

func defaultMP4(t *testing.T) []byte {
	t.Helper()
	b, err := fixture.BuildMP4(fixture.DefaultMP4Options())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRegistryResolveAndVersions(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(NewMP4Adapter()); err != nil {
		t.Fatal(err)
	}
	a, err := r.Resolve(FormatMP4, MP4V1)
	if err != nil {
		t.Fatal(err)
	}
	if a.Key() != (Key{Format: FormatMP4, Major: 1}) {
		t.Fatalf("key = %+v", a.Key())
	}
	_, err = r.Resolve(FormatMP4, 99)
	if !merrors.IsCategory(err, merrors.Unsupported) {
		t.Fatalf("version err = %v", err)
	}
	vs := r.Versions(FormatMP4)
	if len(vs) != 1 || vs[0] != MP4V1 {
		t.Fatalf("versions = %v", vs)
	}
	// Duplicate registration must fail rather than silently override.
	if err := r.Register(NewMP4Adapter()); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestRegistryDetect(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(NewMP4Adapter()); err != nil {
		t.Fatal(err)
	}
	a, err := r.Detect(defaultMP4(t))
	if err != nil {
		t.Fatal(err)
	}
	if a.Key().Format != FormatMP4 {
		t.Fatalf("detected %+v", a.Key())
	}
	if _, err := r.Detect([]byte("not media")); !merrors.IsCategory(err, merrors.Unsupported) {
		t.Fatalf("detect err = %v", err)
	}
}

func TestMP4ParseDurationsAndKeyframes(t *testing.T) {
	a := NewMP4Adapter()
	if !a.Sniff(defaultMP4(t)) {
		t.Fatal("sniff failed")
	}
	res, err := a.Parse(defaultMP4(t))
	if err != nil {
		t.Fatal(err)
	}
	c := res.Container
	if len(c.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(c.Tracks))
	}
	video := c.Tracks[0]
	if video.ID != fixture.VideoTrackID || video.Codec != CodecH264 {
		t.Fatalf("video track = %+v", video)
	}
	if len(video.Durations) != fixture.DefaultMP4Options().VideoSamples {
		t.Fatalf("durations = %d", len(video.Durations))
	}
	// VFR durations must round-trip exactly, including the irregular ones.
	want := fixture.VideoDurations(fixture.DefaultMP4Options().VideoSamples)
	for i, d := range video.Durations {
		if d != uint64(want[i]) {
			t.Fatalf("duration[%d] = %d, want %d", i, d, want[i])
		}
	}
	// Keyframe indices match the generator.
	keys := fixture.Keyframes(len(video.Durations))
	var wantKeys []int
	for i, k := range keys {
		if k {
			wantKeys = append(wantKeys, i)
		}
	}
	if len(video.Keyframes) != len(wantKeys) {
		t.Fatalf("keyframes = %v, want %v", video.Keyframes, wantKeys)
	}
	for i := range wantKeys {
		if video.Keyframes[i] != wantKeys[i] {
			t.Fatalf("keyframe[%d] = %d, want %d", i, video.Keyframes[i], wantKeys[i])
		}
	}
	if len(video.CodecConfig) == 0 {
		t.Fatal("missing codec config")
	}
	audio := c.Tracks[1]
	if audio.Codec != CodecAAC || audio.Timescale != fixture.AudioTimescale {
		t.Fatalf("audio = %+v", audio)
	}
	if audio.SampleCnt != fixture.DefaultMP4Options().AudioSamples {
		t.Fatalf("audio samples = %d", audio.SampleCnt)
	}
	// Sample data bytes are the generated payloads.
	samples := c.Samples[fixture.VideoTrackID]
	for i, s := range samples {
		wantData := fixture.SamplePayload(fixture.VideoTrackID, i, 40+i%7)
		if string(s.Data) != string(wantData) {
			t.Fatalf("sample %d data mismatch", i)
		}
		if s.Duration != uint64(want[i]) {
			t.Fatalf("sample %d duration = %d", i, s.Duration)
		}
	}
}

func TestMP4TruncatedSampleReportsByteOffset(t *testing.T) {
	opt := fixture.DefaultMP4Options()
	opt.TruncateLast = 25
	buf, err := fixture.BuildMP4(opt)
	if err != nil {
		t.Fatal(err)
	}
	a := NewMP4Adapter()
	res, err := a.Parse(buf)
	if err != nil {
		t.Fatalf("truncation should warn, not fail: %v", err)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %d (%v)", len(res.Warnings), res.Warnings)
	}
	w := res.Warnings[0]
	if !merrors.IsCategory(w, merrors.Truncated) {
		t.Fatalf("category = %v (%v)", w, w)
	}
	var me *merrors.Error
	if !asError(w, &me) || !me.HasOff {
		t.Fatalf("missing byte offset: %v", w)
	}
	// The offset must point at the truncated sample, i.e. inside the file.
	if me.Offset <= 0 || me.Offset >= int64(len(buf)) {
		t.Fatalf("offset %d outside file length %d", me.Offset, len(buf))
	}
	// The sample metadata survives so the pipeline can still segment around it.
	video := res.Container.Tracks[0]
	if video.SampleCnt != opt.VideoSamples {
		t.Fatalf("sample count = %d", video.SampleCnt)
	}
	last := res.Container.Samples[fixture.VideoTrackID][len(res.Container.Samples[fixture.VideoTrackID])-1]
	if last.Data != nil {
		t.Fatal("truncated sample should have no data")
	}
}

func TestMP4UnsupportedCodecFailsBeforeWrite(t *testing.T) {
	opt := fixture.DefaultMP4Options()
	opt.UnsupportedCodec = true
	buf, err := fixture.BuildMP4(opt)
	if err != nil {
		t.Fatal(err)
	}
	a := NewMP4Adapter()
	_, err = a.Parse(buf)
	if !merrors.IsCategory(err, merrors.Unsupported) {
		t.Fatalf("parse err = %v", err)
	}
	// Encode must refuse a container whose codec is unsupported.
	c := &Container{
		Format: FormatMP4, Major: MP4V1,
		Tracks:  []TrackInfo{{ID: 1, Codec: Codec("h265"), Timescale: 90000, SampleCnt: 1}},
		Samples: map[uint32][]Sample{1: {{Index: 0, Duration: 3000, IsKeyframe: true, Data: []byte{1}}}},
	}
	if _, err := a.Encode(c); !merrors.IsCategory(err, merrors.Unsupported) {
		t.Fatalf("encode err = %v", err)
	}
}

func TestMP4RejectsUnsupportedMajorOnEncode(t *testing.T) {
	a := NewMP4Adapter()
	c := &Container{Format: FormatMP4, Major: 2, Tracks: []TrackInfo{{ID: 1}}}
	if _, err := a.Encode(c); !merrors.IsCategory(err, merrors.Unsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestMP4RoundTrip(t *testing.T) {
	a := NewMP4Adapter()
	first, err := a.Parse(defaultMP4(t))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Encode(first.Container)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Parse(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if len(second.Container.Tracks) != len(first.Container.Tracks) {
		t.Fatalf("tracks = %d", len(second.Container.Tracks))
	}
	for i := range first.Container.Tracks {
		want := first.Container.Tracks[i]
		got := second.Container.Tracks[i]
		if got.Codec != want.Codec || got.Timescale != want.Timescale || got.SampleCnt != want.SampleCnt {
			t.Fatalf("track %d = %+v, want %+v", i, got, want)
		}
		if len(got.Durations) != len(want.Durations) {
			t.Fatalf("track %d durations %d vs %d", i, len(got.Durations), len(want.Durations))
		}
		for j := range want.Durations {
			if got.Durations[j] != want.Durations[j] {
				t.Fatalf("track %d duration %d = %d, want %d", i, j, got.Durations[j], want.Durations[j])
			}
		}
		wantSamples := first.Container.Samples[want.ID]
		gotSamples := second.Container.Samples[got.ID]
		if len(gotSamples) != len(wantSamples) {
			t.Fatalf("track %d samples %d vs %d", i, len(gotSamples), len(wantSamples))
		}
		for j := range wantSamples {
			if string(gotSamples[j].Data) != string(wantSamples[j].Data) {
				t.Fatalf("track %d sample %d data differs", i, j)
			}
			if gotSamples[j].Duration != wantSamples[j].Duration {
				t.Fatalf("track %d sample %d duration differs", i, j)
			}
			if gotSamples[j].IsKeyframe != wantSamples[j].IsKeyframe {
				t.Fatalf("track %d sample %d keyframe differs", i, j)
			}
		}
	}
}

func TestMP4RoundTripDeterministic(t *testing.T) {
	a := NewMP4Adapter()
	enc := func() []byte {
		res, err := a.Parse(defaultMP4(t))
		if err != nil {
			t.Fatal(err)
		}
		out, err := a.Encode(res.Container)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	x, y := enc(), enc()
	if len(x) != len(y) {
		t.Fatalf("lengths %d vs %d", len(x), len(y))
	}
	for i := range x {
		if x[i] != y[i] {
			t.Fatalf("nondeterministic at byte %d", i)
		}
	}
}

func TestMP4ParseErrors(t *testing.T) {
	a := NewMP4Adapter()
	cases := []struct {
		name string
		buf  []byte
		cat  merrors.Category
	}{
		{"empty", nil, merrors.Malformed},
		{"garbage", []byte("hello world, not mp4"), merrors.Malformed},
		{"oversize", append(defaultMP4(t), make([]byte, MaxMP4Size)...), merrors.Bounds},
	}
	for _, tc := range cases {
		_, err := a.Parse(tc.buf)
		if err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
		if !merrors.IsCategory(err, tc.cat) {
			t.Fatalf("%s: category = %v (%v)", tc.name, err, err)
		}
	}
	// A file cut inside moov must fail cleanly, not panic.
	full := defaultMP4(t)
	if _, err := a.Parse(full[:len(full)/3]); err == nil {
		t.Fatal("expected error for truncated structure")
	}
}

func TestMP4EncodeRejectsIncompleteSamples(t *testing.T) {
	a := NewMP4Adapter()
	base := func() *Container {
		return &Container{
			Format: FormatMP4, Major: MP4V1,
			Tracks:  []TrackInfo{{ID: 1, Codec: CodecH264, Timescale: 90000, SampleCnt: 1, CodecConfig: []byte{1, 2, 3}}},
			Samples: map[uint32][]Sample{1: {{Index: 0, Duration: 3000, IsKeyframe: true, Data: []byte{9}}}},
		}
	}
	if _, err := a.Encode(nil); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("nil err = %v", err)
	}
	c := base()
	c.Tracks[0].Timescale = 0
	if _, err := a.Encode(c); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("timescale err = %v", err)
	}
	c = base()
	c.Samples[1] = []Sample{{Index: 0, Duration: 0, Data: []byte{1}}}
	if _, err := a.Encode(c); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("duration err = %v", err)
	}
	c = base()
	c.Samples[1] = []Sample{{Index: 0, Duration: 3000}}
	if _, err := a.Encode(c); !merrors.IsCategory(err, merrors.Truncated) {
		t.Fatalf("data err = %v", err)
	}
	c = base()
	c.Samples[1] = nil
	if _, err := a.Encode(c); !merrors.IsCategory(err, merrors.Empty) {
		t.Fatalf("empty err = %v", err)
	}
}

func TestSupportsCodecs(t *testing.T) {
	if !Supports(FormatMP4, CodecH264) || !Supports(FormatMP4, CodecAAC) {
		t.Fatal("h264/aac should be supported")
	}
	if Supports(FormatMP4, CodecOpus) || Supports("matroska", CodecH264) {
		t.Fatal("unsupported combination accepted")
	}
}

func asError(err error, target **merrors.Error) bool {
	for err != nil {
		if me, ok := err.(*merrors.Error); ok {
			*target = me
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
