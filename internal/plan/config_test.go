package plan_test

import (
	"testing"

	"mediaflow/internal/plan"
	"mediaflow/internal/policy"
)

func TestConfigDigestDeterministic(t *testing.T) {
	target := policy.Target{
		Width: 1920, Height: 1080, FPS: 30, QualityMin: 0.92,
		BudgetMbps: 18, AudioKbps: 192, Tolerance: 0.05,
	}
	stats := policy.Stats{
		DurationMS: 6400, Width: 1920, Height: 1080, FPS: 30,
		SampleCount: 192, KeyframeCount: 7, MaxKeyframeGapMS: 1000,
		VideoBytes: 6_100_000, AudioBytes: 153_600,
	}
	c1, err := plan.New("sha256:input", target, stats, 64)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := plan.New("sha256:input", target, stats, 64)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Digest == "" || c1.Digest != c2.Digest {
		t.Fatalf("config digests not deterministic: %q vs %q", c1.Digest, c2.Digest)
	}
	c3, err := plan.New("sha256:other", target, stats, 64)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Digest == c3.Digest {
		t.Fatal("input digest change must change config digest")
	}
	if c1.Explain() == "" {
		t.Fatal("explain output should not be empty")
	}
}
