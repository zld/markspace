package track

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediarebuild/internal/sample"
)

func writeTmp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.mp4")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func samples(n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = sample.FrameSample([]byte{byte(i), byte(i * 3), 0x55})
	}
	return out
}

func TestValidTrack(t *testing.T) {
	data := sample.BuildNormalMP4(90000, 3000, samples(4))
	res, err := Validate(context.Background(), writeTmp(t, data))
	if err != nil {
		t.Fatal(err)
	}
	if res.Diags.Count() != 0 {
		t.Fatalf("diags = %v", res.Diags)
	}
	if len(res.Tracks) != 1 {
		t.Fatalf("tracks = %+v", res.Tracks)
	}
	tr := res.Tracks[0]
	if tr.TrackID != 1 || tr.Timescale != 90000 || tr.SampleCount != 4 {
		t.Fatalf("track = %+v", tr)
	}
}

func TestIncompleteIndex(t *testing.T) {
	data := sample.BuildIncompleteIndexMP4(90000, 3000, samples(3))
	res, err := Validate(context.Background(), writeTmp(t, data))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range res.Diags {
		if d.Code == "incomplete-index" && d.Recoverable {
			found = true
		}
	}
	if !found {
		t.Fatalf("diags = %v", res.Diags)
	}
}

func TestNoTracksIsFatal(t *testing.T) {
	// ftyp-only container
	data := sample.BuildNormalMP4(90000, 3000, samples(1))
	// cut everything after ftyp: keep first box only
	size := int(data[0])<<24 | int(data[1])<<16 | int(data[2])<<8 | int(data[3])
	res, err := Validate(context.Background(), writeTmp(t, data[:size]))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Diags.HasFatal() {
		t.Fatalf("expected fatal diag, got %v", res.Diags)
	}
}
