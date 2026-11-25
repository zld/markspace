package migrate

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"example.com/assetcheck/internal/media"
	"example.com/assetcheck/internal/timeline"
)

func assetWithFragments(frags ...media.Fragment) *media.Asset {
	a := &media.Asset{
		Path: "fixture.mkv", Size: 1024, SHA256: "write-fault-input",
		DocType: "matroska", Version: 4, SequenceBase: -1,
	}
	a.Tracks = tracksFixture
	a.Fragments = frags
	return a
}

// TestWriteStageRejectsBadFragmentIndexes proves each ordering defect aborts the
// write stage with its category and the offending block offset, and that no
// generation is published.
func TestWriteStageRejectsBadFragmentIndexes(t *testing.T) {
	cases := []struct {
		name string
		frag media.Fragment
		cat  timeline.Category
	}{
		{"negative duration", media.Fragment{TrackNumber: 1, Order: 1, Start: 10, Duration: -5,
			Offset: 300}, timeline.CatIllegalLength},
		{"negative start", media.Fragment{TrackNumber: 1, Order: 1, Start: -1, Offset: 301},
			timeline.CatIllegalLength},
		{"backwards time", media.Fragment{TrackNumber: 1, Order: 1, Start: 5, Offset: 302},
			timeline.CatOrder},
		{"duplicate sequence", media.Fragment{TrackNumber: 1, Order: 1, Seq: 0, Start: 20,
			Offset: 303}, timeline.CatDuplicate},
		{"sequence gap", media.Fragment{TrackNumber: 1, Order: 1, Seq: 5, Start: 20,
			Offset: 304}, timeline.CatMissingElement},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			m := testManifest(t, manifestV1)
			first := media.Fragment{TrackNumber: 1, Order: 0, Seq: 0, Start: 10, Offset: 200}
			a := assetWithFragments(first)
			if tc.name == "negative duration" || tc.name == "negative start" {
				a.Fragments[0].Seq = -1
			}
			a.Fragments = append(a.Fragments, tc.frag)
			_, err := Run(Options{Out: dir, Manifest: m, Asset: a, StopAfter: StageWrite})
			if err == nil {
				t.Fatal("write stage accepted a bad fragment index")
			}
			var te *timeline.Error
			if !asError(err, &te) {
				t.Fatalf("not structured: %T %v", err, err)
			}
			if te.Stage != timeline.StageWrite {
				t.Fatalf("stage = %s, want write", te.Stage)
			}
			if te.Category != tc.cat {
				t.Fatalf("category = %s, want %s", te.Category, tc.cat)
			}
			if te.Offset != tc.frag.Offset {
				t.Fatalf("offset = %d, want %d", te.Offset, tc.frag.Offset)
			}
			// Nothing may be published, and no staging dir may linger.
			if _, err := os.Stat(filepath.Join(dir, "CURRENT")); !os.IsNotExist(err) {
				t.Fatalf("CURRENT published despite write failure: %v", err)
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if e.IsDir() && len(e.Name()) >= 9 && e.Name()[:9] == ".staging-" {
					t.Fatalf("staging dir leaked: %s", e.Name())
				}
			}
		})
	}
}

// TestWriteStageFailureKeepsPreviousGeneration shows that a later bad write
// cannot overwrite the last valid output.
func TestWriteStageFailureKeepsPreviousGeneration(t *testing.T) {
	dir := t.TempDir()
	m := testManifest(t, manifestV1)
	good := assetWithFragments(
		media.Fragment{TrackNumber: 1, Order: 0, Seq: -1, Start: 0, Offset: 100},
		media.Fragment{TrackNumber: 1, Order: 1, Seq: -1, Start: 10, Offset: 110},
	)
	first, err := Run(Options{Out: dir, Manifest: m, Asset: good})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	bad := assetWithFragments(
		media.Fragment{TrackNumber: 1, Order: 0, Seq: -1, Start: 50, Offset: 200},
		media.Fragment{TrackNumber: 1, Order: 1, Seq: -1, Start: 10, Offset: 210},
	)
	bad.SHA256 = "second-input"
	if _, err := Run(Options{Out: dir, Manifest: m, Asset: bad, StopAfter: StageWrite}); err == nil {
		t.Fatal("bad write reported success")
	}
	store, _ := NewStore(dir)
	_, cur, err := ReadCommitted(store)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Generation != first.Generation {
		t.Fatalf("CURRENT = %s, want the previous generation %s", cur.Generation, first.Generation)
	}
}

func TestFragmentIndexRecordsEveryBlock(t *testing.T) {
	dir := t.TempDir()
	m := testManifest(t, manifestV1)
	a := assetWithFragments(
		media.Fragment{TrackNumber: 1, Order: 0, Seq: -1, Start: 0, Duration: 20, Offset: 100},
		media.Fragment{TrackNumber: 2, Order: 1, Seq: -1, Start: 0, Duration: 20, Offset: 120},
		media.Fragment{TrackNumber: 1, Order: 2, Seq: -1, Start: 20, Duration: 20, Offset: 140},
	)
	res, err := Run(Options{Out: dir, Manifest: m, Asset: a})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, res.Generation, "fragments.jsonl"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	lines := 0
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		lines++
	}
	if lines != 3 {
		t.Fatalf("index lines = %d, want 3:\n%s", lines, data)
	}
	if !bytes.Contains(data, []byte(`"offset":120`)) {
		t.Fatalf("index does not record the raw offset:\n%s", data)
	}
}

func TestWriteFragmentsRejectsReversedSequence(t *testing.T) {
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	a := assetWithFragments(
		media.Fragment{TrackNumber: 1, Order: 0, Seq: 5, Start: 0, Offset: 10},
		media.Fragment{TrackNumber: 1, Order: 1, Seq: 3, Start: 10, Offset: 20},
	)
	err := writeFragments(bw, a)
	if err == nil {
		t.Fatal("reversed sequence accepted")
	}
	if timeline.CategoryOf(err) != timeline.CatMissingElement {
		t.Fatalf("category = %s", timeline.CategoryOf(err))
	}
}
