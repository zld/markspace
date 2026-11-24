package ingest

import (
	"testing"

	"mediamerge/internal/fixgen"
	"mediamerge/internal/model"
)

func parseSpec(t *testing.T, name string, spec fixgen.FileSpec) (*File, *model.Errors) {
	t.Helper()
	errs := &model.Errors{}
	f, fatal := ParseFile(name, fixgen.Build(spec), errs)
	if fatal != nil {
		t.Fatalf("fatal parse: %v", fatal)
	}
	return f, errs
}

func audioTrack() fixgen.TrackSpec {
	return fixgen.TrackSpec{ID: 2, Handler: "soun", Timescale: 48000, Channels: 2, SampleRate: 48000}
}

func frag(seq, track uint32, tfdt uint64, n int) fixgen.FragSpec {
	f := fixgen.FragSpec{Seq: seq, TrackID: track, TFDT: tfdt}
	for i := 0; i < n; i++ {
		f.Samples = append(f.Samples, fixgen.SampleSpec{Data: []byte{byte(i)}, Duration: 960})
	}
	return f
}

func TestRecoverMfhdGap(t *testing.T) {
	spec := fixgen.FileSpec{
		Tracks: []fixgen.TrackSpec{audioTrack()},
		Frags:  []fixgen.FragSpec{frag(1, 2, 0, 4), frag(2, 2, 3840, 4), frag(5, 2, 15360, 4)},
	}
	f, errs := parseSpec(t, "gap.mp4", spec)
	res := Recover([]*File{f}, nil, errs)
	if len(res.Gaps) != 1 {
		t.Fatalf("gaps=%d", len(res.Gaps))
	}
	g := res.Gaps[0]
	if g.AfterSeq != 2 || g.BeforeSeq != 5 || g.TrackID != 2 {
		t.Fatalf("gap: %+v", g)
	}
	if g.EstSamples != 2*4 {
		t.Fatalf("est=%d", g.EstSamples)
	}
	// No fabricated samples: 3 fragments x 4 samples only.
	if len(res.Samples) != 12 {
		t.Fatalf("samples=%d", len(res.Samples))
	}
	// Gap interval must sit between fragment 2 end and fragment 5 start.
	if g.Time.Start <= 0 || g.Time.End <= g.Time.Start {
		t.Fatalf("gap interval: %+v", g.Time)
	}
}

func TestRecoverTfdtRollback(t *testing.T) {
	spec := fixgen.FileSpec{
		Tracks: []fixgen.TrackSpec{audioTrack()},
		// frag 3 rolls tfdt back to 0; seq chain is continuous so the
		// expected decode time (7680) is unambiguous.
		Frags: []fixgen.FragSpec{frag(1, 2, 0, 4), frag(2, 2, 3840, 4), frag(3, 2, 0, 4)},
	}
	f, errs := parseSpec(t, "roll.mp4", spec)
	res := Recover([]*File{f}, nil, errs)
	if len(res.IsolatedTracks) != 0 {
		t.Fatalf("isolated=%v", res.IsolatedTracks)
	}
	if len(res.RecoveredTracks) != 1 || res.RecoveredTracks[0] != 2 {
		t.Fatalf("recovered=%v", res.RecoveredTracks)
	}
	if bad := model.CheckMonotonic(res.Samples); bad >= 0 {
		t.Fatalf("non-monotonic at %d", bad)
	}
	// Recovered frag 3 must start where frag 2 ended: 7680 ticks.
	last := res.Samples[len(res.Samples)-4]
	want, _ := model.Timescale{TicksPerSecond: 48000}.ToNs(7680)
	if last.Start != want {
		t.Fatalf("recovered start=%d want=%d", last.Start, want)
	}
}

func TestRecoverRollbackAcrossGapIsolates(t *testing.T) {
	spec := fixgen.FileSpec{
		Tracks: []fixgen.TrackSpec{audioTrack(),
			{ID: 3, Handler: "soun", Timescale: 48000, Channels: 1, SampleRate: 48000}},
		Frags: []fixgen.FragSpec{
			frag(1, 2, 0, 4), frag(4, 2, 100, 4), // gap + rollback: ambiguous
			frag(1, 3, 0, 4), frag(2, 3, 3840, 4), // healthy track
		},
	}
	f, errs := parseSpec(t, "iso.mp4", spec)
	res := Recover([]*File{f}, nil, errs)
	if len(res.IsolatedTracks) != 1 || res.IsolatedTracks[0] != 2 {
		t.Fatalf("isolated=%v", res.IsolatedTracks)
	}
	// Unrelated track 3 still processed.
	for _, s := range res.Samples {
		if s.TrackID == 2 {
			t.Fatal("isolated track leaked samples")
		}
	}
	if len(res.Samples) != 8 {
		t.Fatalf("samples=%d", len(res.Samples))
	}
}

func TestRecoverDuplicateFragment(t *testing.T) {
	spec := fixgen.FileSpec{
		Tracks: []fixgen.TrackSpec{audioTrack()},
		Frags:  []fixgen.FragSpec{frag(1, 2, 0, 4), frag(1, 2, 0, 4), frag(2, 2, 3840, 4)},
	}
	f, errs := parseSpec(t, "dup.mp4", spec)
	res := Recover([]*File{f}, nil, errs)
	if len(res.Samples) != 8 {
		t.Fatalf("samples=%d (duplicate not dropped)", len(res.Samples))
	}
}

func TestRecoverRTPContradictionIsolates(t *testing.T) {
	spec := fixgen.FileSpec{
		Tracks: []fixgen.TrackSpec{audioTrack()},
		Frags:  []fixgen.FragSpec{frag(1, 2, 0, 4), frag(2, 2, 3840, 4)},
	}
	f, errs := parseSpec(t, "rtp.mp4", spec)
	// RTP capture whose unrolled timeline decreases: contradiction.
	rtpData := fixgen.BuildRTP([]fixgen.RTPSpec{
		{Seq: 1, Timestamp: 5000, SSRC: 2, Payload: []byte{1}},
		{Seq: 2, Timestamp: 4000, SSRC: 2, Payload: []byte{1}},
	})
	events := ParseRTP("rtp.bin", rtpData, errs)
	// Force a non-monotonic capture by direct construction.
	events[1].Unrolled = events[0].Unrolled - 1
	res := Recover([]*File{f}, map[uint32][]RTPEvent{2: events}, errs)
	if len(res.IsolatedTracks) != 1 {
		t.Fatalf("isolated=%v", res.IsolatedTracks)
	}
}

func TestRTPParseAndRollover(t *testing.T) {
	rtpData := fixgen.BuildRTP([]fixgen.RTPSpec{
		{Seq: 65534, Timestamp: 0xfffffff0, SSRC: 7, Payload: []byte{1, 2}},
		{Seq: 65535, Timestamp: 0xffffffff, SSRC: 7, Payload: []byte{1}},
		{Seq: 0, Timestamp: 10, SSRC: 7, Payload: []byte{1}},
	})
	var errs model.Errors
	events := ParseRTP("cap.bin", rtpData, &errs)
	if len(events) != 3 {
		t.Fatalf("events=%d", len(events))
	}
	if events[2].Unrolled <= events[1].Unrolled {
		t.Fatalf("rollover not unrolled: %d -> %d", events[1].Unrolled, events[2].Unrolled)
	}
	if !Monotonic(events) {
		t.Fatal("expected monotone unrolled timeline")
	}
}

func TestRecoverDeterministic(t *testing.T) {
	spec := fixgen.FileSpec{
		Tracks: []fixgen.TrackSpec{audioTrack()},
		Frags:  []fixgen.FragSpec{frag(1, 2, 0, 4), frag(2, 2, 0, 4), frag(3, 2, 7680, 4)},
	}
	run := func() *Result {
		f, errs := parseSpec(t, "det.mp4", spec)
		return Recover([]*File{f}, nil, errs)
	}
	r1, r2 := run(), run()
	if len(r1.Samples) != len(r2.Samples) {
		t.Fatal("sample count differs between runs")
	}
	for i := range r1.Samples {
		if r1.Samples[i] != r2.Samples[i] {
			t.Fatalf("sample %d differs", i)
		}
	}
}
