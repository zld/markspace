package manifest

import (
	"testing"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

const legacyJSON = `{
  "version": 1,
  "entries": [
    {"id": "v0", "codec": "av1", "path": "v0.obu", "time_ms": 0, "duration_ms": 33},
    {"id": "v1", "codec": "av1", "path": "v1.obu", "time_ms": 33, "duration_ms": 33}
  ]
}`

const currentJSON = `{
  "version": 2,
  "tracks": [
    {"name": "video", "timescale": 90000},
    {"name": "audio", "timescale": 48000}
  ],
  "entries": [
    {"id": "v0", "codec": "av1", "path": "v0.obu", "track": "video", "pts": 0, "duration": 3003},
    {"id": "v1", "codec": "av1", "path": "v1.obu", "track": "video", "pts": 3003, "duration": 3003},
    {"id": "a0", "codec": "aac", "path": "a0.aac", "track": "audio", "pts": 0, "duration": 1024}
  ]
}`

func TestDecodeLegacy(t *testing.T) {
	m, err := Decode([]byte(legacyJSON))
	if err != nil {
		t.Fatal(err)
	}
	if m.SourceVersion != VersionLegacy {
		t.Fatalf("version = %d", m.SourceVersion)
	}
	if len(m.Items) != 2 {
		t.Fatalf("items = %d", len(m.Items))
	}
	if m.Items[1].PTS != 33*clock.Millisecond {
		t.Fatalf("pts = %s want 33ms", m.Items[1].PTS)
	}
	if m.Items[0].Track != "video" || m.Items[0].SourceUnit != "ms" {
		t.Fatalf("item = %+v", m.Items[0])
	}
	if m.Duration() != 66*clock.Millisecond {
		t.Fatalf("duration = %s", m.Duration())
	}
}

func TestDecodeCurrent(t *testing.T) {
	m, err := Decode([]byte(currentJSON))
	if err != nil {
		t.Fatal(err)
	}
	if m.SourceVersion != VersionCurrent || len(m.Items) != 3 {
		t.Fatalf("manifest = %+v", m)
	}
	if m.Items[1].PTS != clock.Time(33366667) {
		t.Fatalf("pts = %d", m.Items[1].PTS)
	}
	tracks := m.Tracks()
	if len(tracks) != 2 || tracks[0] != "video" || tracks[1] != "audio" {
		t.Fatalf("tracks = %v", tracks)
	}
	if got := m.TrackDuration("audio"); got != 21333333 {
		t.Fatalf("audio duration = %d", got)
	}
}

func TestDecodeRejectsUnknownVersion(t *testing.T) {
	_, err := Decode([]byte(`{"version": 3, "entries": []}`))
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeManifestVersion {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeRejectsEmptyAndMalformed(t *testing.T) {
	for _, doc := range []string{"", "{", `{"version":1,"entries":[]}`} {
		_, err := Decode([]byte(doc))
		if err == nil {
			t.Fatalf("doc %q should fail", doc)
		}
	}
}

func TestDecodeRejectsDuplicateID(t *testing.T) {
	doc := `{"version":1,"entries":[{"id":"x","codec":"av1","time_ms":0,"duration_ms":1},{"id":"x","codec":"av1","time_ms":1,"duration_ms":1}]}`
	_, err := Decode([]byte(doc))
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeManifestDuplicateID {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeRejectsUnknownTrack(t *testing.T) {
	doc := `{"version":2,"tracks":[{"name":"video","timescale":90000}],"entries":[{"id":"a","codec":"av1","track":"audio","pts":1,"duration":1}]}`
	_, err := Decode([]byte(doc))
	if err == nil {
		t.Fatal("unknown track must fail")
	}
}
