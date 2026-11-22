package fixtures

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediaarchive/internal/stream"
	"mediaarchive/internal/subtitle"
)

func TestGenerateIsByteDeterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	sa, err := Generate(a, SetAll)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := Generate(b, SetAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(sa.Assets) != len(sb.Assets) || len(sa.Assets) < 10 {
		t.Fatalf("asset sets differ: %d vs %d", len(sa.Assets), len(sb.Assets))
	}
	for i := range sa.Assets {
		if sa.Assets[i].Name != sb.Assets[i].Name {
			t.Fatalf("asset order differs at %d", i)
		}
		for _, pair := range [][2]string{
			{sa.Assets[i].MP4, sb.Assets[i].MP4},
			{sa.Assets[i].Subtitle, sb.Assets[i].Subtitle},
			{sa.Assets[i].RTP, sb.Assets[i].RTP},
		} {
			if pair[0] == "" {
				continue
			}
			da, err := os.ReadFile(pair[0])
			if err != nil {
				t.Fatal(err)
			}
			db, err := os.ReadFile(pair[1])
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(da, db) {
				t.Fatalf("%s not deterministic", filepath.Base(pair[0]))
			}
		}
	}
}

func TestEverySetGenerates(t *testing.T) {
	sets := []string{SetAll, SetNormal, SetDamaged, SetDrift, SetVFR, SetWrap, SetTrunc, SetRTP, SetSubtitle}
	for _, s := range sets {
		set, err := Generate(t.TempDir(), s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if len(set.Assets) == 0 {
			t.Fatalf("%s produced no assets", s)
		}
	}
	if _, err := Generate(t.TempDir(), "nope"); err == nil {
		t.Fatal("unknown set accepted")
	}
}

func TestFixturesAreAnalyzable(t *testing.T) {
	dir := t.TempDir()
	set, err := Generate(dir, SetAll)
	if err != nil {
		t.Fatal(err)
	}
	normalCues, damagedCues := 0, 0
	mp4s, videos, damaged := 0, 0, 0
	for _, a := range set.Assets {
		if a.MP4 != "" {
			mp4s++
			an, err := stream.AnalyzeMP4(a.MP4)
			if err != nil {
				t.Fatalf("%s: %v", a.Name, err)
			}
			if an.Video != nil && len(an.Video.Samples) > 0 {
				videos++
				if an.Video.MaxKeyframeGapMs() < 100 || an.Video.MaxKeyframeGapMs() > 5000 {
					t.Fatalf("%s gap = %v", a.Name, an.Video.MaxKeyframeGapMs())
				}
			}
			if len(an.Faults) > 0 {
				damaged++
			}
		}
		if a.Subtitle != "" {
			res, err := subtitle.ReadFile(a.Subtitle)
			if err != nil {
				t.Fatal(err)
			}
			if a.Tag == "normal" {
				normalCues += len(res.Cues)
				if len(res.Faults) > 0 {
					t.Fatalf("%s normal fixture has faults: %v", a.Name, res.Faults)
				}
			}
			damagedCues += len(res.Faults)
		}
	}
	if mp4s < 6 || videos < 6 {
		t.Fatalf("mp4s=%d videos=%d", mp4s, videos)
	}
	if normalCues < 10 {
		t.Fatalf("normal cues = %d", normalCues)
	}
	// Two FR-recoverable cue faults plus the truncated tail record, which is a
	// fault rather than a cue, and at least one structurally damaged MP4.
	if damagedCues < 2 || damaged < 1 {
		t.Fatalf("damaged cues=%d damaged mp4=%d", damagedCues, damaged)
	}
}

func TestRTPFixturesExerciseWrapAndLoss(t *testing.T) {
	dir := t.TempDir()
	set, err := Generate(dir, SetAll)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range set.Assets {
		if a.RTP == "" {
			continue
		}
		res, err := stream.AnalyzeRTPFile(context.Background(), a.RTP, stream.RTPOptions{ClockRate: 90000})
		if err != nil {
			t.Fatalf("%s: %v", a.Name, err)
		}
		if res.Total < 500 {
			t.Fatalf("%s packets = %d", a.Name, res.Total)
		}
		switch a.Tag {
		case "wrap":
			if res.Analysis.Missing != 0 || len(res.Analysis.Faults) != 0 {
				t.Fatalf("wrap fixture faults: %v", res.Analysis.Faults)
			}
			if res.Analysis.DurationMs() < 30000 {
				t.Fatalf("wrap duration = %v", res.Analysis.DurationMs())
			}
		case "rtp":
			if res.Analysis.Missing == 0 || res.Analysis.Duplicates == 0 || res.Analysis.OutOfOrder == 0 {
				t.Fatalf("loss fixture: %s", res)
			}
			if res.Analysis.TimestampJumps == 0 {
				t.Fatalf("loss fixture lacks timestamp jump")
			}
		}
	}
}
