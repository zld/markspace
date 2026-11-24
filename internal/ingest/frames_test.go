package ingest

import (
	"path/filepath"
	"testing"

	"mediarc/internal/model"
)

func TestGenerateScanLocatesFrameDamageExactly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frames.mrc1")
	fx, err := GenerateFrameFile(FrameGenOptions{
		Path: path, Seed: 17, Frames: 24,
		BadSHA: true, Truncated: true, OutOfOrder: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	faults := &model.Collector{}
	metas, err := ScanFrameFile(path, faults)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 24 {
		t.Fatalf("records=%d want 24", len(metas))
	}
	bad := faults.All()
	var sha, trunc *model.Fault
	for i := range bad {
		switch bad[i].Kind {
		case model.FaultBadSHA:
			sha = &bad[i]
		case model.FaultTruncated:
			trunc = &bad[i]
		}
	}
	if sha == nil || trunc == nil {
		t.Fatalf("missing faults: %+v", bad)
	}
	if sha.MaterialID != fx.MaterialID || sha.RecordIndex != int64(fx.BadSHA.LogicalIndex) {
		t.Fatalf("bad sha identity wrong: %+v fx=%+v", sha, fx.BadSHA)
	}
	if sha.Offset != fx.BadSHA.Offset {
		t.Fatalf("bad sha offset=%d want %d", sha.Offset, fx.BadSHA.Offset)
	}
	if trunc.Offset != fx.Truncated.Offset {
		t.Fatalf("truncate offset=%d want %d", trunc.Offset, fx.Truncated.Offset)
	}
	if locErr := abs64(sha.Offset - fx.BadSHA.Offset); locErr > 1 {
		t.Fatalf("sha location error=%d", locErr)
	}
	if locErr := abs64(trunc.Offset - fx.Truncated.Offset); locErr > 1 {
		t.Fatalf("truncate location error=%d", locErr)
	}

	var valid int
	for _, m := range metas {
		if m.Valid {
			valid++
		}
	}
	if valid != 22 {
		t.Fatalf("valid=%d want 22; processing must continue after damage", valid)
	}
}

func TestReadFramePayloadRechecksChecksum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frames.mrc1")
	fx, err := GenerateFrameFile(FrameGenOptions{Path: path, Seed: 3, Frames: 4, OutOfOrder: true})
	if err != nil {
		t.Fatal(err)
	}
	faults := &model.Collector{}
	metas, err := ScanFrameFile(path, faults)
	if err != nil {
		t.Fatal(err)
	}
	idx := fx.SwappedPairs[0][0] + 1
	if _, err := ReadFramePayload(path, metas[idx]); err != nil {
		t.Fatalf("read valid payload: %v", err)
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
