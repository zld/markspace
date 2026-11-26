package index_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediarepair/internal/index"
	"mediarepair/internal/media"
	"mediarepair/internal/testfixture"
)

func buildTestIndex(t *testing.T, root string) *index.Index {
	t.Helper()
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		t.Fatal(err)
	}
	var recs []index.Record
	for i, f := range fx.Manifest.Frames {
		recs = append(recs, index.Record{
			Sequence:       f.Sequence,
			Track:          f.Track,
			RepairedTimeUS: media.ExpectedMicroseconds(i, fx.Manifest.FrameRate),
			RawTicks:       f.RawTicks,
			SampleOffset:   int64(i * 100),
			SampleSize:     uint32(len(fx.FrameBytes[i])),
			FrameSHA256:    f.SHA256,
			RelativePath:   f.Path,
		})
	}
	idx, err := index.Build("input-test", recs, 4)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestIndexBlocksLocateAndFrameVerification(t *testing.T) {
	root := t.TempDir()
	idx := buildTestIndex(t, root)
	if len(idx.Records) != 12 || len(idx.Blocks) != 3 {
		t.Fatalf("records=%d blocks=%d", len(idx.Records), len(idx.Blocks))
	}
	path := filepath.Join(root, "index.json")
	if err := idx.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := index.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := loaded.LocateAtOrBefore(166667)
	if !ok || rec.Sequence != 5 {
		t.Fatalf("locate 166667 => %+v ok=%v", rec, ok)
	}
	rec, ok = loaded.LocateSequence(9)
	if !ok || rec.BlockIndex != 2 || rec.BlockChecksum == "" {
		t.Fatalf("sequence 9 record=%+v", rec)
	}
	if err := rec.VerifyFrame(root); err != nil {
		t.Fatal(err)
	}
}

func TestIndexDetectsTamperedBlock(t *testing.T) {
	root := t.TempDir()
	idx := buildTestIndex(t, root)
	idx.Records[2].FrameSHA256 = "bad"
	if err := idx.Verify(); !media.HasCode(err, media.CodeVerification) {
		t.Fatalf("verify err=%v", err)
	}
}

func TestIndexVerifyFrameDetectsReplacement(t *testing.T) {
	root := t.TempDir()
	idx := buildTestIndex(t, root)
	rec := idx.Records[0]
	path, err := filepath.Abs(filepath.Join(root, rec.RelativePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := appendByte(path); err != nil {
		t.Fatal(err)
	}
	if err := rec.VerifyFrame(root); !media.HasCode(err, media.CodeChecksum) {
		t.Fatalf("frame verify err=%v", err)
	}
}

func appendByte(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write([]byte{0})
	return err
}
