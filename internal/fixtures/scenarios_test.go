package fixtures

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abema/go-mp4"
)

func TestGenerateAllScenarios(t *testing.T) {
	dir := t.TempDir()
	gens, err := Generate(dir, 41)
	if err != nil {
		t.Fatal(err)
	}
	if len(gens) != len(AllScenarios) {
		t.Fatalf("got %d scenarios", len(gens))
	}
	for _, g := range gens {
		if _, err := os.Stat(g.MP4Path); err != nil {
			t.Fatalf("%s: %v", g.Scenario, err)
		}
		if _, err := os.Stat(g.Manifest); err != nil {
			t.Fatalf("%s manifest: %v", g.Scenario, err)
		}
	}
	idx, err := os.Stat(filepath.Join(dir, "index.json"))
	if err != nil || idx.Size() == 0 {
		t.Fatalf("index.json: %v", err)
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	d1 := t.TempDir()
	d2 := t.TempDir()
	g1, err := Generate(d1, 99)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := Generate(d2, 99)
	if err != nil {
		t.Fatal(err)
	}
	for i := range g1 {
		if g1[i].SHA256 != g2[i].SHA256 {
			t.Fatalf("%s: hashes differ across identical seeds", g1[i].Scenario)
		}
	}
}

func TestGenerateDifferentSeedChangesBytes(t *testing.T) {
	d1 := t.TempDir()
	d2 := t.TempDir()
	g1, _ := Generate(d1, 1)
	g2, _ := Generate(d2, 2)
	if g1[0].SHA256 == g2[0].SHA256 {
		t.Fatal("different seeds produced identical audio")
	}
}

func TestScenariosProbe(t *testing.T) {
	dir := t.TempDir()
	gens, err := Generate(dir, 41)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gens {
		raw, err := os.ReadFile(g.MP4Path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := mp4.Probe(strings.NewReader(string(raw)))
		if err != nil {
			// truncated and corrupt cases may legitimately fail probing
			if g.ExpectTruncated || g.ExpectIndexGap {
				continue
			}
			t.Fatalf("%s probe: %v", g.Scenario, err)
		}
		if g.ExpectTruncated || g.ExpectIndexGap {
			continue
		}
		if len(info.Tracks) != 1 {
			t.Fatalf("%s tracks %d", g.Scenario, len(info.Tracks))
		}
		if info.Tracks[0].Timescale != uint32(g.SampleRate) {
			t.Fatalf("%s timescale %d", g.Scenario, info.Tracks[0].Timescale)
		}
	}
}

func TestTruncatedIsShorter(t *testing.T) {
	dir := t.TempDir()
	gens, err := Generate(dir, 41)
	if err != nil {
		t.Fatal(err)
	}
	var norm, trunc *Generated
	for i := range gens {
		switch gens[i].Scenario {
		case ScenarioNormal:
			norm = &gens[i]
		case ScenarioTruncated:
			trunc = &gens[i]
		}
	}
	n, _ := os.Stat(norm.MP4Path)
	tr, _ := os.Stat(trunc.MP4Path)
	if tr.Size() >= n.Size() {
		t.Fatal("truncated fixture is not smaller than the intact one")
	}
	raw, _ := os.ReadFile(trunc.MP4Path)
	// The cut must land inside mdat, not after it.
	if info, err := mp4.Probe(strings.NewReader(string(raw))); err == nil && info.Tracks[0].Chunks[0].DataOffset == 0 {
		t.Fatal("truncated file unexpectedly reports valid chunks")
	}
}

func TestSparseHasEditsAndNoStco(t *testing.T) {
	dir := t.TempDir()
	gens, _ := Generate(dir, 41)
	var sparse *Generated
	for i := range gens {
		if gens[i].Scenario == ScenarioSparse {
			sparse = &gens[i]
		}
	}
	raw, _ := os.ReadFile(sparse.MP4Path)
	if hasBox(raw, "stco") {
		t.Fatal("sparse fixture must omit stco")
	}
	if !hasBox(raw, "elst") {
		t.Fatal("sparse fixture must carry an edit list")
	}
	if !hasBox(raw, "stss") {
		t.Fatal("sparse fixture must declare sparse keyframes")
	}
}

func TestHotIsFivePointOne(t *testing.T) {
	dir := t.TempDir()
	gens, _ := Generate(dir, 41)
	for _, g := range gens {
		if g.Scenario != ScenarioHot {
			continue
		}
		if g.Channels != 6 || g.Layout != "5.1" {
			t.Fatalf("hot fixture layout %d/%s", g.Channels, g.Layout)
		}
	}
}

func hasBox(raw []byte, name string) bool {
	return strings.Contains(string(raw), name)
}
