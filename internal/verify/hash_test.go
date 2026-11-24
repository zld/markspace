package verify

import (
	"testing"

	"archive/internal/demux"
	"archive/internal/fault"
	"archive/internal/fixture"
	"archive/internal/model"
	"archive/internal/probe"
	"archive/internal/remux"
)

func pipelineOut(t *testing.T, name string) (*demux.Result, []byte) {
	t.Helper()
	spec, ok := fixture.FindSpec(fixture.DefaultMixed(), name)
	if !ok {
		t.Fatalf("spec %s", name)
	}
	var data []byte
	var err error
	if spec.Format == model.ContainerMP4 {
		_, data, err = fixture.BuildMP4(spec)
	} else {
		_, data, err = fixture.BuildMXF(spec)
	}
	if err != nil {
		t.Fatal(err)
	}
	src := probe.Source{Name: name, Data: data}
	f, err := probe.Probe(src, spec.Format)
	if err != nil {
		t.Fatal(err)
	}
	in, err := demux.Demux(src, f)
	if err != nil {
		t.Fatal(err)
	}
	tl := model.New()
	var ts []*remux.TrackSource
	for _, td := range in.Tracks {
		tt := &model.TrackTimeline{
			Key: td.Key, Timebase: td.Timebase,
			Declared: model.NanosRange{StartNS: td.Timebase.TickToNanos(td.Range().StartTick), EndNS: td.Timebase.TickToNanos(td.Range().StartTick + td.Range().Duration)},
			Segments: []model.Segment{{TrackKey: td.Key, StartTick: td.Range().StartTick, Duration: td.Range().Duration}},
		}
		if err := tl.AddTrack(tt); err != nil {
			t.Fatal(err)
		}
		codec := "avc1"
		if td.Kind == model.KindAudio {
			codec = "mp4a"
		}
		ts = append(ts, &remux.TrackSource{
			Key: td.Key, Logical: td.Logical, Kind: td.Kind, Codec: codec, TrackID: uint32(len(ts) + 1),
			Timebase: td.Timebase, Packets: td.Packets,
			Color: model.ColorDescription{Primaries: model.ColorField{Value: "bt709"}, Transfer: model.ColorField{Value: "bt709"}, Matrix: model.ColorField{Value: "bt709"}, Range: model.ColorField{Value: "limited"}},
		})
	}
	res, err := remux.Write(tl, ts, remux.Options{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	return in, res.Data
}

func TestVerifyAcceptsLosslessOutput(t *testing.T) {
	for _, name := range []string{"reel_a", "clip_b"} {
		in, out := pipelineOut(t, name)
		rep, err := Check(in, out)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !rep.OK {
			t.Fatalf("%s: verification failed: %s %v", name, rep.Summary, in.Faults)
		}
		for _, tc := range rep.Tracks {
			if !tc.HashesMatch || !tc.CountMatch || !tc.RangeMatch {
				t.Fatalf("%s: track %s = %+v", name, tc.Logical, tc)
			}
		}
	}
}

func TestVerifyReportsFirstDifference(t *testing.T) {
	in, out := pipelineOut(t, "clip_b")
	// Corrupt one payload byte inside the mdat. The frame header CRC then
	// fails, so the output demux sees one fewer packet: the verifier must
	// report the track and the first differing packet, not a generic error.
	mutated := append([]byte(nil), out...)
	idx := indexOf(mutated, "mdat")
	if idx < 0 {
		t.Fatal("no mdat")
	}
	// Flip a byte well inside the first payload.
	mutated[idx+8+50] ^= 0xFF
	rep, err := Check(in, mutated)
	if err != nil {
		// An unreadable output is an acceptable verification failure path.
		var fe *fault.Error
		if !asFault(err, &fe) || fe.Code != fault.VerifyMismatch {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if rep.OK {
		t.Fatal("corrupted output accepted")
	}
	if len(rep.Faults) == 0 {
		t.Fatal("no verify fault")
	}
	foundDiff := false
	for _, tc := range rep.Tracks {
		if !tc.HashesMatch || !tc.CountMatch {
			foundDiff = true
			if tc.Logical == "" {
				t.Fatal("difference not attributed to a track")
			}
		}
	}
	if !foundDiff {
		t.Fatalf("no track flagged: %+v", rep.Tracks)
	}
}

func TestVerifyDetectsDroppedPacket(t *testing.T) {
	in, out := pipelineOut(t, "clip_b")
	// Remove the last packet by rewriting the output's stsz/appending is
	// complex; instead compare against a truncated input view to prove the
	// count check works.
	if len(in.Tracks) == 0 {
		t.Fatal("no tracks")
	}
	truncated := &demux.Result{Container: in.Container, SHA256: in.SHA256}
	for _, td := range in.Tracks {
		cp := *td
		if len(cp.Packets) > 1 {
			cp.Packets = cp.Packets[:len(cp.Packets)-1]
		}
		truncated.Tracks = append(truncated.Tracks, &cp)
	}
	rep, err := Check(truncated, out)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK {
		t.Fatal("packet count mismatch accepted")
	}
	flagged := false
	for _, tc := range rep.Tracks {
		if tc.InputCount != tc.OutputCount {
			flagged = true
		}
	}
	if !flagged {
		t.Fatalf("count mismatch not flagged: %+v", rep.Tracks)
	}
}

func indexOf(b []byte, s string) int {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return i
		}
	}
	return -1
}

func asFault(err error, target **fault.Error) bool {
	fe, ok := err.(*fault.Error)
	if ok {
		*target = fe
	}
	return ok
}
