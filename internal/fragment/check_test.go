package fragment

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediarebuild/internal/sample"
	"mediarebuild/internal/track"
)

func writeTmp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "frag.mp4")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func frags() [][][]byte {
	mk := func(base byte, n int) [][]byte {
		out := make([][]byte, n)
		for i := range out {
			out[i] = sample.FrameSample([]byte{base, byte(i), 0x11, 0x22})
		}
		return out
	}
	return [][][]byte{mk(1, 3), mk(9, 2)}
}

func TestCleanFragments(t *testing.T) {
	data := sample.BuildFragmentedMP4(90000, 3000, frags())
	p := writeTmp(t, data)
	tv, err := track.Validate(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Check(context.Background(), p, []uint32{tv.Tracks[0].TrackID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Diags.Count() != 0 {
		t.Fatalf("diags = %v", res.Diags)
	}
	if res.Fragments != 2 || res.Samples != 5 {
		t.Fatalf("fragments=%d samples=%d, want 2/5", res.Fragments, res.Samples)
	}
}

func TestCorruptSampleReportsInjectedOffset(t *testing.T) {
	data := sample.BuildFragmentedMP4(90000, 3000, frags())
	corrupt, injected := sample.CorruptSampleStart(data, 1, 1)
	res, err := Check(context.Background(), writeTmp(t, corrupt), []uint32{1})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range res.Diags {
		if d.Code == "corrupt-sample" {
			found = true
			if d.Offset != injected {
				t.Fatalf("diag offset %d, injected at %d", d.Offset, injected)
			}
		}
	}
	if !found {
		t.Fatalf("no corrupt-sample diag in %v", res.Diags)
	}
}

func TestUnknownTrackReference(t *testing.T) {
	data := sample.BuildFragmentedMP4(90000, 3000, frags())
	res, err := Check(context.Background(), writeTmp(t, data), []uint32{99})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range res.Diags {
		if d.Code == "unknown-track" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diags = %v", res.Diags)
	}
}
