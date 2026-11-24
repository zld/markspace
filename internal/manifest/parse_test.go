package manifest

import (
	"path/filepath"
	"strings"
	"testing"

	"mediad/internal/clock"
)

func validManifest() *Manifest {
	return &Manifest{
		Version:        1,
		SessionStartNs: 1_700_000_000_000_000_000,
		Entries: []Entry{{
			Camera: "cam-a",
			File:   "media/cam-a.mp4",
			Tracks: []TrackSpec{
				{TrackID: 1, Kind: "video", Timescale: 90000},
				{TrackID: 2, Kind: "audio", Timescale: 48000, StartNs: 20_000_000},
			},
			Recording: Interval{StartNs: 0, EndNs: 600_000_000_000},
			Editorial: Interval{StartNs: 90_000_000_000, EndNs: 150_000_000_000},
		}},
	}
}

func TestValidateAcceptance(t *testing.T) {
	m := validManifest()
	if err := Validate(m, "/tmp/root"); err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 1 || m.Entries[0].SourceAbs == "" {
		t.Fatalf("source path not resolved: %+v", m.Entries[0])
	}
	base, err := m.Entries[0].TrackBase(m.Entries[0].Tracks[1])
	if err != nil {
		t.Fatal(err)
	}
	if base.OriginNs != 20_000_000 {
		t.Fatalf("track origin lost: %d", base.OriginNs)
	}
	if base.Timescale != 48000 {
		t.Fatalf("timescale=%d", base.Timescale)
	}
}

func TestValidateRejectsUnknownField(t *testing.T) {
	_, err := Parse(strings.NewReader(`{"version":1,"bogus":1,"entries":[]}`), t.TempDir())
	if err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestValidateRejectsSmallerAndBadIntervals(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Manifest)
	}{
		{"zero recording", func(m *Manifest) { m.Entries[0].Recording = Interval{} }},
		{"editorial outside", func(m *Manifest) { m.Entries[0].Editorial.EndNs = m.Entries[0].Recording.EndNs + 1 }},
		{"no tracks", func(m *Manifest) { m.Entries[0].Tracks = nil }},
		{"duplicate track", func(m *Manifest) {
			m.Entries[0].Tracks = append(m.Entries[0].Tracks, m.Entries[0].Tracks[0])
		}},
		{"bad kind", func(m *Manifest) { m.Entries[0].Tracks[0].Kind = "subtitle" }},
		{"zero timescale", func(m *Manifest) { m.Entries[0].Tracks[0].Timescale = 0 }},
		{"negative session", func(m *Manifest) { m.SessionStartNs = -1 }},
		{"duplicate camera", func(m *Manifest) { m.Entries = append(m.Entries, m.Entries[0]) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := validManifest()
			c.mut(m)
			err := Validate(m, "/tmp/root")
			if err == nil {
				t.Fatal("expected rejection")
			}
			pe, ok := clock.As(err)
			if !ok || pe.Stage != clock.StageParse {
				t.Fatalf("error not stage-tagged: %v", err)
			}
		})
	}
}

func TestPathEscapeRejected(t *testing.T) {
	for _, p := range []string{"../secret.mp4", "media/../../secret.mp4", "/etc/passwd", "media/../..//x.mp4"} {
		m := validManifest()
		m.Entries[0].File = p
		err := Validate(m, "/tmp/root")
		if err == nil {
			t.Fatalf("path %q not rejected", p)
		}
		pe, ok := clock.As(err)
		if !ok || pe.Code != clock.CodePathEscape {
			t.Fatalf("path %q: unexpected error %v", p, err)
		}
		if pe.ManifestLine != 1 {
			t.Fatalf("missing manifest line: %+v", pe)
		}
	}
}

func TestNestedPathAccepted(t *testing.T) {
	m := validManifest()
	m.Entries[0].File = "deep/nested/clip.mp4"
	if err := Validate(m, "/tmp/root"); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/root", "deep/nested/clip.mp4")
	if m.Entries[0].SourceAbs != want {
		t.Fatalf("SourceAbs=%q want %q", m.Entries[0].SourceAbs, want)
	}
}

func TestOversizedManifest(t *testing.T) {
	m := validManifest()
	for i := 0; i < MaxEntries+1; i++ {
		e := m.Entries[0]
		e.Camera = "cam-" + itoa(i)
		m.Entries = append(m.Entries, e)
	}
	err := Validate(m, "/tmp/root")
	pe, ok := clock.As(err)
	if !ok || pe.Code != clock.CodeOversizedTable {
		t.Fatalf("expected oversized table, got %v", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

func TestTraversalEndToEnd(t *testing.T) {
	// A manifest whose source path climbs out of the root must fail validation
	// with path_escape before any file is read.
	m := validManifest()
	m.Entries[0].File = "../../etc/passwd"
	err := Validate(m, "/tmp/media-root")
	pe, ok := clock.As(err)
	if !ok || pe.Code != clock.CodePathEscape {
		t.Fatalf("unexpected: %v", err)
	}
	if pe.ManifestLine == 0 {
		t.Fatal("escape must report its manifest line")
	}
}
