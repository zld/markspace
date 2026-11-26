package demo

import (
	"strings"
	"testing"

	"mediasync/internal/clock"
	"mediasync/internal/report"
	"mediasync/internal/validate"
)

func TestRunProducesAllScenarios(t *testing.T) {
	samples, err := Run(7, clock.FromMilliseconds(2000))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"normal", "truncated-mp4", "corrupt-rtp", "illegal-df", "late-in-window", "late-out-of-window"}
	if len(samples) != len(want) {
		t.Fatalf("samples=%d want %d", len(samples), len(want))
	}
	for i, name := range want {
		if samples[i].Name != name {
			t.Fatalf("sample %d = %s want %s", i, samples[i].Name, name)
		}
	}
}

func TestDemoDigestsAreRunIndependent(t *testing.T) {
	first, err := Run(7, clock.FromMilliseconds(2000))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(7, clock.FromMilliseconds(2000))
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		if first[i].Digest != second[i].Digest {
			t.Fatalf("%s digest changed between runs: %s vs %s",
				first[i].Name, first[i].Digest, second[i].Digest)
		}
		if first[i].Report != second[i].Report {
			t.Fatalf("%s report changed between runs", first[i].Name)
		}
	}
	// The report must not leak the temporary fixture directory.
	for _, s := range first {
		if strings.Contains(s.Report, "mediasync-demo-") {
			t.Fatalf("%s report contains temp path", s.Name)
		}
	}
}

func TestDemoSeedChangesContent(t *testing.T) {
	a, err := Run(7, clock.FromMilliseconds(2000))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run(8, clock.FromMilliseconds(2000))
	if err != nil {
		t.Fatal(err)
	}
	if a[0].Digest == b[0].Digest {
		t.Fatal("different seeds produced the same normal digest")
	}
}

func TestNormalWithinOneTick(t *testing.T) {
	samples, err := Run(7, clock.FromMilliseconds(2000))
	if err != nil {
		t.Fatal(err)
	}
	norm := samples[0]
	if len(norm.Result.Issues) != 0 {
		t.Fatalf("normal issues: %v", norm.Result.Issues)
	}
	if norm.Result.MaxTimeDev > 1 {
		t.Fatalf("normal deviation %s", norm.Result.MaxTimeDev)
	}
}

func TestInWindowEqualsBaseline(t *testing.T) {
	samples, err := Run(7, clock.FromMilliseconds(2000))
	if err != nil {
		t.Fatal(err)
	}
	var in validate.Result
	for _, s := range samples {
		if s.Name == "late-in-window" {
			in = *s.Result
			if s.Digest != s.BaselineDigest {
				t.Fatalf("in-window digest %s != baseline %s", s.Digest, s.BaselineDigest)
			}
		}
	}
	if in.Recomputed == 0 {
		t.Fatal("in-window late event triggered no recomputation")
	}
	if in.Unrecoverable != 0 {
		t.Fatalf("in-window event counted unrecoverable: %d", in.Unrecoverable)
	}
}

func TestOutOfWindowUnrecoverable(t *testing.T) {
	samples, err := Run(7, clock.FromMilliseconds(2000))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range samples {
		if s.Name == "late-out-of-window" {
			if s.Result.Unrecoverable == 0 || s.Result.First == nil {
				t.Fatalf("no unrecoverable evidence: %+v", s.Result)
			}
			if s.Result.First.Loc.Line == 0 || s.Result.First.Loc.Input == "" {
				t.Fatalf("unrecoverable location missing: %+v", s.Result.First.Loc)
			}
		}
	}
}

func TestDamagedSamplesReportByteLocations(t *testing.T) {
	samples, err := Run(7, clock.FromMilliseconds(2000))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range samples {
		switch s.Name {
		case "truncated-mp4", "corrupt-rtp":
			if len(s.Result.Issues) == 0 {
				t.Fatalf("%s reported no issues", s.Name)
			}
			if s.Result.First == nil || s.Result.First.Loc.ByteOffset < 0 {
				t.Fatalf("%s first failure not byte-localised", s.Name)
			}
			if len(s.Result.Damaged) == 0 {
				t.Fatalf("%s has no damaged ranges", s.Name)
			}
		}
	}
}

func TestSeedChangesByteContentNotStructure(t *testing.T) {
	// The normal sample's structural summary must not depend on the seed;
	// only payload sizes (and therefore byte deviation) may vary.
	a, _ := Run(7, clock.FromMilliseconds(2000))
	b, _ := Run(11, clock.FromMilliseconds(2000))
	sa := report.BuildSnapshot(a[0].Result)
	sb := report.BuildSnapshot(b[0].Result)
	if sa.Keyframes != sb.Keyframes {
		t.Fatalf("keyframe stats depend on seed: %+v vs %+v", sa.Keyframes, sb.Keyframes)
	}
	if sa.EventCount != sb.EventCount {
		t.Fatalf("event count depends on seed")
	}
}
