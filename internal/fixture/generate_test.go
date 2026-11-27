package fixture

import (
	"os"
	"testing"

	"mediad/internal/clock"
	"mediad/internal/ebml"
)

func parseFile(t *testing.T, path string) ([]*ebml.Node, error) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return ebml.NewParser(data).Parse()
}

func TestGeneratedFixturesAreDeterministic(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	if _, err := Generate(d1); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(d2); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{ValidMKV, TruncatedMKV, InvalidVINTMKV, OutOfBoundsMKV, RTPEvents, TasksOK, TasksConflict, MP4Check} {
		b1, err := os.ReadFile(d1 + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		b2, err := os.ReadFile(d2 + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if string(b1) != string(b2) {
			t.Fatalf("fixture %s not deterministic", name)
		}
	}
}

func TestValidFixtureParses(t *testing.T) {
	dir := t.TempDir()
	set, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := parseFile(t, set.Path(ValidMKV))
	if err != nil {
		t.Fatal(err)
	}
	var seg *ebml.Node
	for _, n := range nodes {
		if n.ID == ebml.IDSegment {
			seg = n
		}
	}
	if seg == nil {
		t.Fatal("no segment")
	}
	scale, tracks, err := ebml.SegmentInfo(seg)
	if err != nil {
		t.Fatal(err)
	}
	if scale != TimecodeScaleNs || len(tracks) != 2 {
		t.Fatalf("scale=%d tracks=%v", scale, tracks)
	}
	events, err := ebml.Events(seg)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 12 { // 3 clusters x 4 blocks
		t.Fatalf("got %d events, want 12", len(events))
	}
	var keys int
	for _, ev := range events {
		if ev.Keyframe {
			keys++
		}
	}
	if keys != 3 {
		t.Fatalf("got %d keyframes, want 3", keys)
	}
}

func TestCorruptFixturesFailWithStableCodes(t *testing.T) {
	dir := t.TempDir()
	set, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		code clock.Code
	}{
		{TruncatedMKV, clock.ErrTruncatedHeader},
		{InvalidVINTMKV, clock.ErrInvalidVINT},
		{OutOfBoundsMKV, clock.ErrBounds},
	}
	for _, c := range cases {
		_, err := parseFile(t, set.Path(c.name))
		e, ok := err.(*clock.Error)
		if !ok || e.Code != c.code {
			t.Fatalf("%s: want %s, got %v", c.name, c.code, err)
		}
		if e.Offset < 0 || e.Path == "" {
			t.Fatalf("%s: error lacks localization: %v", c.name, e)
		}
	}
}
