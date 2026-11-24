package fixtures

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateCreatesAllScenariosWithManifests(t *testing.T) {
	root := t.TempDir()
	got, err := Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("scenarios=%d", len(got))
	}
	for _, name := range Scenarios() {
		m := got[name]
		if m.MP4 == "" || m.Timecodes == "" || len(m.Subtitles) == 0 {
			t.Fatalf("%s manifest incomplete: %+v", name, m)
		}
		for _, p := range append([]string{m.MP4, m.Timecodes}, mapValues(m.Subtitles)...) {
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		// Manifests round-trip through disk byte-for-byte.
		raw, err := os.ReadFile(filepath.Join(m.Dir, ManifestName))
		if err != nil {
			t.Fatal(err)
		}
		var back Manifest
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if back.MP4 != m.MP4 || back.TimecodeMode != m.TimecodeMode {
			t.Fatalf("%s manifest mismatch", name)
		}
		if _, err := LoadManifest(m.Dir); err != nil {
			t.Fatal(err)
		}
	}
	normal := got[ScenarioNormal]
	if len(normal.RTP) != 1 {
		t.Fatalf("normal RTP tracks %v", normal.RTP)
	}
	edge := got[ScenarioEdge]
	if len(edge.RTP) != 2 || edge.TimecodeMode != "29.97df" {
		t.Fatalf("edge manifest %+v", edge)
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if _, err := Generate(a); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(b); err != nil {
		t.Fatal(err)
	}
	for _, name := range Scenarios() {
		for _, file := range []string{"video.mp4", "captions.srt", "programme.tc"} {
			pa, pb := filepath.Join(a, name, file), filepath.Join(b, name, file)
			da, erra := os.ReadFile(pa)
			db, errb := os.ReadFile(pb)
			if (erra == nil) != (errb == nil) {
				t.Fatalf("%s/%s present in one run only", name, file)
			}
			if erra == nil && !bytes.Equal(da, db) {
				t.Fatalf("%s/%s differs between runs", name, file)
			}
		}
	}
}

func TestBrokenScenarioDamageIsTargeted(t *testing.T) {
	dir := t.TempDir()
	m, err := GenerateScenario(dir, ScenarioBroken)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.MP4)
	if err != nil {
		t.Fatal(err)
	}
	// The keyframe index box still exists and is still named stss.
	if !bytes.Contains(data, []byte("stss")) {
		t.Fatal("broken fixture should keep the stss box readable")
	}
	// The first stss entry must be impossible.
	idx := bytes.Index(data, []byte("stss")) - 4
	entryCount := int(uint32(data[idx+12])<<24 | uint32(data[idx+13])<<16 | uint32(data[idx+14])<<8 | uint32(data[idx+15]))
	if entryCount == 0 {
		t.Fatal("stss has no entries")
	}
	first := uint32(data[idx+16])<<24 | uint32(data[idx+17])<<16 | uint32(data[idx+18])<<8 | uint32(data[idx+19])
	if first <= 40 {
		t.Fatalf("first entry %d should be out of range for a 40-sample clip", first)
	}
	if !bytes.Contains([]byte(m.Notes[0]), []byte("damaged stss")) {
		t.Fatalf("notes should record the damage: %v", m.Notes)
	}
}

func TestEdgeScenarioHasVFRAndWrap(t *testing.T) {
	dir := t.TempDir()
	m, err := GenerateScenario(dir, ScenarioEdge)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.RTP["a1-wrap"])
	if err != nil {
		t.Fatal(err)
	}
	h, samples, err := ParseRTPStream(data)
	if err != nil {
		t.Fatal(err)
	}
	if h.Timescale != 90000 || len(samples) != 30 {
		t.Fatalf("header %+v samples %d", h, len(samples))
	}
	// Find a timestamp decrease, which is what forces a wrap.
	wrapped := false
	for i := 1; i < len(samples); i++ {
		if samples[i].Packet.Timestamp < samples[i-1].Packet.Timestamp {
			wrapped = true
		}
	}
	if !wrapped {
		t.Fatal("wrap fixture never crosses the 32-bit boundary")
	}
	drift, err := os.ReadFile(m.RTP["a2-drift"])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(drift, data) {
		t.Fatal("drift fixture must differ from the wrap fixture")
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
