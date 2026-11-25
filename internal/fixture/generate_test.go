package fixture_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/fixture"
)

func TestGenerateAllCreatesNormalAndNegativeFixtures(t *testing.T) {
	b, err := fixture.GenerateAll(filepath.Join(t.TempDir(), "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		b.NormalDASH,
		b.NormalHLS,
		b.MisalignedDASH,
		b.ZeroTimescale,
		b.RegressionHLS,
		b.Truncated.Manifest,
		b.IllegalSample.Manifest,
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("fixture missing %s: %v", p, err)
		}
	}
	if b.Truncated.ByteOffset <= 0 || b.IllegalSample.ByteOffset <= 0 {
		t.Fatalf("expected byte coordinates: trunc=%d illegal=%d", b.Truncated.ByteOffset, b.IllegalSample.ByteOffset)
	}
	if b.Truncated.ExpectedExitCode != 2 || len(b.Cases) < 4 {
		t.Fatalf("unexpected acceptance cases: %+v", b)
	}
}

func TestBuildFragmentRecordsSampleOffsets(t *testing.T) {
	info, err := fixture.BuildFragment(fixture.FragmentSpec{
		Sequence: 1,
		Samples:  []fixture.SampleSpec{{Duration: 100, Size: 8}, {Duration: 100, Size: 12}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(info.SampleOffsets) != 2 || info.SampleOffsets[1] != info.SampleOffsets[0]+8 {
		t.Fatalf("bad offsets %+v", info.SampleOffsets)
	}
	if int(info.SampleOffsets[1]+12-info.SampleOffsets[0]) > len(info.Bytes) {
		t.Fatal("fragment bytes do not contain declared samples")
	}
}
