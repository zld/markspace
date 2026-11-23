package align

import (
	"testing"

	"mediadeliver/internal/model"
)

func TestSortedInputsAndSchema(t *testing.T) {
	mp := mustMPD(t, genMPD("PT0S", "PT8S"))
	pl := mustPL(t, fourSecondPlaylist)
	m := Build(Input{
		MPD: mp, Playlist: pl,
		Inputs: []InputRef{
			{Role: "media", Representation: "v720", Path: "inputs/v720.mp4", SHA256: "b"},
			{Role: "hls", Path: "inputs/media.m3u8", SHA256: "h"},
			{Role: "mpd", Path: "inputs/manifest.mpd", SHA256: "m"},
			{Role: "media", Representation: "a48", Path: "inputs/a48.mp4", SHA256: "a"},
		},
	})
	if m.Schema != Schema {
		t.Fatalf("schema = %q", m.Schema)
	}
	wantRoles := []string{"mpd", "hls", "media", "media"}
	for i, want := range wantRoles {
		if m.Inputs[i].Role != want {
			t.Fatalf("input %d role = %s, want %s", i, m.Inputs[i].Role, want)
		}
	}
	if m.Inputs[2].Representation != "a48" || m.Inputs[3].Representation != "v720" {
		t.Fatalf("media inputs not sorted: %+v", m.Inputs[2:])
	}
}

func TestMissingInputsProduceDiagnostics(t *testing.T) {
	m := Build(Input{})
	if m.Summary.Deliverable {
		t.Fatal("empty input reported deliverable")
	}
	if len(m.Errors) == 0 || m.Errors[0].Code != model.CodeAlignInput {
		t.Fatalf("diagnostics = %v", m.Errors)
	}
}

func TestSummaryCounts(t *testing.T) {
	mp := mustMPD(t, genMPD("PT0S", "PT8S"))
	pl := mustPL(t, fourSecondPlaylist)
	m := Build(Input{MPD: mp, Playlist: pl})
	if m.Summary.Representations != 3 || m.Summary.Intervals != 2 {
		t.Fatalf("summary = %+v", m.Summary)
	}
	for _, iv := range m.Intervals {
		if len(iv.Entries) != 3 {
			t.Fatalf("interval %d has %d entries", iv.Index, len(iv.Entries))
		}
	}
}
