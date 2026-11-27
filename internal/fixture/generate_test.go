package fixture

import (
	"context"
	"os"
	"testing"

	"mediad/internal/ingest"
	"mediad/internal/model"
	"mediad/internal/pipeline"
	"mediad/internal/quality"
)

func TestGenerateValidSamples(t *testing.T) {
	for _, codec := range []string{ingest.CodecH264, ingest.CodecH265, ingest.CodecAV1} {
		codec := codec
		t.Run(codec, func(t *testing.T) {
			s, err := Generate(DefaultParams(codec, 17))
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Stream) == 0 || s.Index == nil || len(s.Index.Entries) == 0 {
				t.Fatal("sample missing stream or index")
			}
			// The generated stream must pass the real scanner and index checks.
			sc := ingest.NewScanner(ingest.ReaderConfig{})
			res, err := sc.Scan(context.Background(), codec, s.Stream)
			if err != nil {
				t.Fatalf("scanner rejects generated %s stream: %v", codec, err)
			}
			if res.FirstKeyOffset < 0 {
				t.Fatal("no key frame detected in generated stream")
			}
			if _, err := ingest.ValidateIndex(codec, s.Index.Entries, int64(len(s.Stream))); err != nil {
				t.Fatalf("index invalid: %v", err)
			}
			if len(s.Frames) != 30 || len(s.DerivedFrames) != 30 {
				t.Fatal("pixel frame count mismatch")
			}
			if s.DurationNS <= 0 {
				t.Fatal("duration must be positive")
			}
		})
	}
}

func TestGenerateDeterministicFromSeed(t *testing.T) {
	a, err := Generate(DefaultParams(ingest.CodecH264, 17))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(DefaultParams(ingest.CodecH264, 17))
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Stream) != string(b.Stream) {
		t.Fatal("identical seed must produce identical streams")
	}
	c, err := Generate(DefaultParams(ingest.CodecH264, 18))
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Stream) == string(c.Stream) {
		t.Fatal("different seeds must produce different streams")
	}
}

func TestGeneratedBitrateTracksTarget(t *testing.T) {
	s, err := Generate(DefaultParams(ingest.CodecAV1, 7))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := quality.Bitrate(int64(len(s.Stream)), s.DurationNS)
	if err != nil {
		t.Fatal(err)
	}
	pct := quality.BitrateErrorPct(actual, s.TargetBPS)
	if pct > 5.0 {
		t.Fatalf("bitrate error %.4f%% exceeds 5%% (target=%d actual=%d)", pct, s.TargetBPS, actual)
	}
}

func TestGeneratedQualityMeetsBars(t *testing.T) {
	s, err := Generate(DefaultParams(ingest.CodecH264, 11))
	if err != nil {
		t.Fatal(err)
	}
	w, h := int(s.Width), int(s.Height)
	var psnrs, ssims []float64
	for i := range s.Frames {
		a := &quality.Frame{Width: w, Height: h, Stride: w, Pix: s.Frames[i]}
		b := &quality.Frame{Width: w, Height: h, Stride: w, Pix: s.DerivedFrames[i]}
		p, err := quality.PSNR(a, b)
		if err != nil {
			t.Fatal(err)
		}
		sim, err := quality.SSIM(a, b)
		if err != nil {
			t.Fatal(err)
		}
		psnrs = append(psnrs, p)
		ssims = append(ssims, sim)
	}
	if got := quality.Aggregate(psnrs); got < 30 {
		t.Fatalf("PSNR %.2f below 30 dB bar", got)
	}
	if got := quality.Aggregate(ssims); got < 0.95 {
		t.Fatalf("SSIM %.4f below 0.95 bar", got)
	}
}

func TestCorruptVariants(t *testing.T) {
	valid, err := Generate(DefaultParams(ingest.CodecH264, 5))
	if err != nil {
		t.Fatal(err)
	}
	variants, err := Corrupt(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(variants) != 4 {
		t.Fatalf("want 4 corrupt variants, got %d", len(variants))
	}
	for _, v := range variants {
		t.Run(string(v.Kind), func(t *testing.T) {
			switch v.Kind {
			case CorruptIndexOutOfRange:
				if _, err := ingest.ValidateIndex(v.Codec, v.Index.Entries, int64(len(v.Stream))); err == nil {
					t.Fatal("out-of-range index must be rejected")
				}
			case CorruptTruncatedUnit, CorruptIllegalHeader:
				sc := ingest.NewScanner(ingest.ReaderConfig{})
				_, err := sc.Scan(context.Background(), v.Codec, v.Stream)
				if err == nil {
					t.Fatal("corrupt stream must fail scanning")
				}
				se, ok := model.AsStructured(err)
				if !ok || se.Code != v.WantCode {
					t.Fatalf("want %s, got %v", v.WantCode, err)
				}
			case CorruptZeroTimebase:
				if err := v.FPS.Validate(false); err == nil {
					t.Fatal("zero time base must fail validation")
				}
			}
		})
	}
}

func TestInterruptionResidue(t *testing.T) {
	root := t.TempDir()
	if err := WriteInterruptionResidue(root, "srcX", "h264"); err != nil {
		t.Fatal(err)
	}
	eng, err := pipeline.NewEngine(root)
	if err != nil {
		t.Fatal(err)
	}
	// The uncommitted normalize directory with the partial temp file exists.
	partial := eng.StageDir("srcX", pipeline.StageNormalize) + "/partial.tmp"
	if _, err := os.Stat(partial); err != nil {
		t.Fatalf("interruption residue missing: %v", err)
	}
	// Ingest is already committed and must survive.
	if err := eng.VerifyCommitted(mustLoad(t, eng, "srcX"), pipeline.StageIngest); err != nil {
		t.Fatalf("committed ingest should verify: %v", err)
	}
}

func mustLoad(t *testing.T, eng *pipeline.Engine, sha string) *pipeline.State {
	t.Helper()
	st, err := eng.LoadState(sha, "h264")
	if err != nil {
		t.Fatal(err)
	}
	return st
}
