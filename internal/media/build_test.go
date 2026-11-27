package media

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"
)

func TestBuildProducesProbeableAsset(t *testing.T) {
	essence := EssenceFor("audio-main", "v1", 1, 64)
	raw, err := Build(Asset{
		TrackID:       1,
		Timescale:     1000,
		DurationTicks: 120,
		Essence:       essence,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	info, err := mp4.Probe(bytesReader(raw))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Timescale != 1000 || info.Duration != 120 {
		t.Fatalf("timescale/duration = %d/%d", info.Timescale, info.Duration)
	}
	if len(info.Tracks) != 1 || info.Tracks[0].TrackID != 1 {
		t.Fatalf("tracks=%+v", info.Tracks)
	}
	if len(info.Tracks[0].Samples) != 1 {
		t.Fatalf("samples=%d", len(info.Tracks[0].Samples))
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	asset := Asset{TrackID: 1, Timescale: 48000, DurationTicks: 9600, Essence: EssenceFor("a", "v1", 0, 32)}
	a, err := Build(asset)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(asset)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("identical assets must build byte-identical files")
	}
}

func TestEssenceDiffersByVersionAndSalt(t *testing.T) {
	base := EssenceFor("audio-main", "v1", 0, 48)
	sameLength := EssenceFor("audio-main", "v2", 0, 48)
	if string(base) == string(sameLength) {
		t.Fatal("different versions should differ")
	}
	salted := EssenceFor("audio-main", "v1", 9, 48)
	if string(base) == string(salted) {
		t.Fatal("salt should change content")
	}
	if len(salted) != len(base) {
		t.Fatal("salt must not change length")
	}
}

func TestBuildRejectsInvalidInput(t *testing.T) {
	cases := []Asset{
		{TrackID: 1, DurationTicks: 1, Essence: []byte{1}},                  // zero timescale
		{TrackID: 0, Timescale: 1000, DurationTicks: 1, Essence: []byte{1}}, // zero track
		{TrackID: 1, Timescale: 1000, DurationTicks: 1},                     // empty essence
	}
	for i, c := range cases {
		if _, err := Build(c); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
