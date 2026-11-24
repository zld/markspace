package remux

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"archive/internal/demux"
	"archive/internal/fixture"
	"archive/internal/model"
	"archive/internal/probe"
)

func sources(t *testing.T, name string) (*model.Timeline, []*TrackSource, []byte) {
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
	res, err := demux.Demux(src, f)
	if err != nil {
		t.Fatal(err)
	}
	tl := model.New()
	var ts []*TrackSource
	for _, td := range res.Tracks {
		tt := &model.TrackTimeline{
			Key: td.Key, Container: string(spec.Format), TrackID: 1, Timebase: td.Timebase,
			Declared: model.NanosRange{
				StartNS: td.Timebase.TickToNanos(td.Range().StartTick),
				EndNS:   td.Timebase.TickToNanos(td.Range().StartTick + td.Range().Duration),
			},
			Segments: []model.Segment{{
				TrackKey: td.Key, StartTick: td.Range().StartTick, Duration: td.Range().Duration,
			}},
		}
		if err := tl.AddTrack(tt); err != nil {
			t.Fatal(err)
		}
		source := &TrackSource{
			Key: td.Key, Logical: td.Logical, Kind: td.Kind, TrackID: uint32(len(ts) + 1),
			Codec: "avc1", Timebase: td.Timebase, Packets: td.Packets,
		}
		if td.Kind == model.KindAudio {
			source.Codec = "mp4a"
		}
		if td.Kind == model.KindVideo {
			source.Crop = &model.Crop{Right: 1920, Bottom: 1080}
			source.Color = model.ColorDescription{
				Primaries: model.ColorField{Value: "bt709"}, Transfer: model.ColorField{Value: "bt709"},
				Matrix: model.ColorField{Value: "bt709"}, Range: model.ColorField{Value: "limited"},
			}
		}
		ts = append(ts, source)
	}
	return tl, ts, data
}

func TestRemuxIsLossless(t *testing.T) {
	for _, name := range []string{"reel_a", "clip_b"} {
		tl, ts, _ := sources(t, name)
		res, err := Write(tl, ts, Options{Workers: 3})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Fragments == 0 {
			t.Fatalf("%s: no fragments", name)
		}
		// Re-probe and demux the output and compare every payload hash.
		src := probe.Source{Name: "out", Data: res.Data}
		f, err := probe.Probe(src, model.ContainerMP4)
		if err != nil {
			t.Fatalf("%s: output unreadable: %v", name, err)
		}
		if len(f.Faults) != 0 {
			t.Fatalf("%s: output faults: %v", name, probe.DescribeFaults(f.Faults))
		}
		out, err := demux.Demux(src, f)
		if err != nil {
			t.Fatalf("%s: output demux: %v", name, err)
		}
		before := map[string][]string{}
		for _, td := range ts {
			for _, p := range td.Packets {
				before[td.Logical] = append(before[td.Logical], p.SHA256)
			}
		}
		for _, td := range out.Tracks {
			got := []string{}
			for _, p := range td.Packets {
				got = append(got, p.SHA256)
			}
			if len(got) != len(before[td.Logical]) {
				t.Fatalf("%s/%s: %d packets, want %d", name, td.Logical, len(got), len(before[td.Logical]))
			}
			for i := range got {
				if got[i] != before[td.Logical][i] {
					t.Fatalf("%s/%s packet %d hash mismatch", name, td.Logical, i)
				}
			}
		}
	}
}

func TestRemuxDeterministic(t *testing.T) {
	tl, ts, _ := sources(t, "clip_b")
	a, err := Write(tl, ts, Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Write(tl, ts, Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Data, b.Data) {
		t.Fatal("output is not deterministic")
	}
	if a.SHA256 != b.SHA256 {
		t.Fatal("digest differs")
	}
}

func TestRemuxInterruptionKeepsCommitsAndDoesNotEmitOutput(t *testing.T) {
	tl, ts, _ := sources(t, "clip_b")
	var commits []CommittedFragment
	res, err := Write(tl, ts, Options{
		Workers:   2,
		FailTrack: ts[0].Key,
		FailAfter: 2,
		OnCommit: func(_ TrackResult, cf CommittedFragment) error {
			commits = append(commits, cf)
			return nil
		},
	})
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if len(res.Data) != 0 {
		t.Fatal("interrupted run must not produce output bytes")
	}
	if len(commits) == 0 {
		t.Fatal("no commits recorded before interruption")
	}
	// Resume: pass the committed fragments back; they must be reused, not
	// rewritten, and the final output must equal the uninterrupted run.
	committed := map[string][]CommittedFragment{}
	for _, cf := range commits {
		committed[cf.TrackKey] = append(committed[cf.TrackKey], cf)
	}
	resumed, err := Write(tl, ts, Options{Workers: 2, Committed: committed})
	if err != nil {
		t.Fatal(err)
	}
	clean, err := Write(tl, ts, Options{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SHA256 != clean.SHA256 {
		t.Fatalf("resumed output differs: %s vs %s", resumed.SHA256, clean.SHA256)
	}
	// No fragment appears twice in the resumed result.
	seen := map[string]bool{}
	for _, tr := range resumed.Tracks {
		for _, cf := range tr.Committed {
			key := cf.TrackKey + "/" + itoa(cf.Seq)
			if seen[key] {
				t.Fatalf("fragment %s committed twice", key)
			}
			seen[key] = true
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestRemuxRejectsFragmentOutsideTimeline(t *testing.T) {
	tl, ts, _ := sources(t, "clip_b")
	// Shrink the timeline so the second packet falls outside it.
	for _, tt := range tl.Tracks {
		for i := range tt.Segments {
			tt.Segments[i].Duration = 1
		}
	}
	_, err := Write(tl, ts, Options{Workers: 1})
	if err == nil {
		t.Fatal("expected out-of-timeline commit to fail")
	}
}

func TestRemuxRejectsNoTracks(t *testing.T) {
	_, err := Write(model.New(), nil, Options{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRemuxOutputDigestMatchesBytes(t *testing.T) {
	tl, ts, _ := sources(t, "reel_a")
	res, err := Write(tl, ts, Options{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(res.Data)
	if hex.EncodeToString(sum[:]) != res.SHA256 {
		t.Fatal("reported digest does not match output")
	}
}
