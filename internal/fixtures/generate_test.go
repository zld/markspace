package fixtures

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"medialab/internal/ingest"
	"medialab/internal/model"
	"medialab/internal/pixel"
)

func TestGenerateDeterministic(t *testing.T) {
	var hashes []string
	for run := 0; run < 2; run++ {
		dir := t.TempDir()
		ds, err := Generate(DefaultSpec(CaseNormal, 7, dir))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		files, err := os.ReadDir(filepath.Join(dir, "frames"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			data, err := os.ReadFile(filepath.Join(dir, "frames", f.Name()))
			if err != nil {
				t.Fatal(err)
			}
			h.Write(data)
		}
		manifest, err := os.ReadFile(ds.Manifest)
		if err != nil {
			t.Fatal(err)
		}
		h.Write(manifest)
		hashes = append(hashes, hex.EncodeToString(h.Sum(nil)))
	}
	if hashes[0] != hashes[1] {
		t.Fatalf("fixture hashes differ: %s vs %s", hashes[0], hashes[1])
	}
}

func TestGenerateFramesDecode(t *testing.T) {
	dir := t.TempDir()
	ds, err := Generate(DefaultSpec(CaseNormal, 11, dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Frames) != 15 {
		t.Fatalf("frames = %d", len(ds.Frames))
	}
	for _, f := range ds.Frames {
		data, err := os.ReadFile(filepath.Join(dir, f.Path))
		if err != nil {
			t.Fatal(err)
		}
		if hashHex(data) != f.SHA256 {
			t.Fatalf("frame %d/%d hash mismatch", f.Track, f.Sequence)
		}
		raw, track, seq, err := pixel.Decode(data)
		if err != nil {
			t.Fatalf("decode %s: %v", f.Path, err)
		}
		if raw.W != f.Width || raw.H != f.Height || int(track) != int(f.Track) || seq != f.Sequence {
			t.Fatalf("frame %s metadata mismatch", f.Path)
		}
	}
}

func TestGenerateCorruptHashNamesTrack(t *testing.T) {
	dir := t.TempDir()
	ds, err := Generate(DefaultSpec(CaseCorruptHash, 7, dir))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ingest.Load(ds.Manifest, ingest.LoadOptions{RootDir: dir, VerifyContent: true})
	f, ok := model.AsFault(err)
	if !ok || f.Code != model.CodeHashMismatch {
		t.Fatalf("err = %v", err)
	}
	if f.Track != 2 || f.Frame != 2 {
		t.Fatalf("location = track %d frame %d", f.Track, f.Frame)
	}
	if ds.Note == "" {
		t.Fatal("case note missing")
	}
}

func TestGenerateReorderStreamIsShuffled(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(DefaultSpec(CaseReorderLoss, 7, dir)); err != nil {
		t.Fatal(err)
	}
	pkts, err := ReadRTP(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkts) == 0 {
		t.Fatal("no packets")
	}
	// The generated stream must not already be in ascending sequence order,
	// otherwise the reorder case proves nothing.
	shuffled := false
	for i := 1; i < len(pkts); i++ {
		if pkts[i].Seq < pkts[i-1].Seq {
			shuffled = true
			break
		}
	}
	if !shuffled {
		t.Fatal("reorder fixture is already ordered")
	}
}

func TestReadRTPRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ds, err := Generate(DefaultSpec(CaseNormal, 3, dir))
	if err != nil {
		t.Fatal(err)
	}
	for tr := 1; tr <= ds.Spec.Tracks; tr++ {
		pkts, err := ReadRTP(dir, model.TrackID(tr))
		if err != nil {
			t.Fatal(err)
		}
		assembled, warns := ingest.Assemble(pkts)
		if len(warns) != 0 {
			t.Fatalf("track %d warnings: %v", tr, warns)
		}
		if len(assembled) != ds.Spec.FramesEach {
			t.Fatalf("track %d frames = %d", tr, len(assembled))
		}
	}
}

func TestReadRTPRejectsTruncation(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(DefaultSpec(CaseNormal, 1, dir)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rtp", "track-1.rtp")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw[:3], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRTP(dir, 1); err == nil {
		t.Fatal("truncated stream accepted")
	}
}
