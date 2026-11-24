package report

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestSummaryPrintsStableRequiredKeys(t *testing.T) {
	s := Summary{
		Mode: "resume", FrameTotal: 120, BadSHA: 1, Truncated: 1,
		LatePackets: 2, DuplicatePackets: 3, LocationErrorMax: 0,
		ChunkCount: 8, HaveResume: true, ResumedFromChunk: 3,
		HaveResumeTiming: true, ResumeElapsedMS: 12, Completed: true,
		OutputSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	var buf bytes.Buffer
	s.Print(&buf)
	out := buf.String()
	for _, want := range []string{
		"frame_total=120", "bad_sha=1", "truncated=1", "late_packets=2",
		"duplicate_packets=3", "定位误差=0", "chunk_count=8",
		"resumed_from_chunk=3", "resume_elapsed_ms=12", "恢复耗时毫秒=12",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if !regexp.MustCompile(`(?m)^sha256=[0-9a-f]{64}$`).MatchString(out) {
		t.Fatal("sha line is not 64 lowercase hex")
	}
}
