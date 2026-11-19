package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"mediakey/internal/reason"
)

func sampleReport() *Report {
	return &Report{
		AssetID:       "asset-demo",
		RotationCount: 3,
		License: LicenseResult{
			Serial: "lic-1", KeyMin: 1, KeyMax: 4,
			Status: StatusOK, Reason: reason.NONE,
		},
		Rotations: []Rotation{
			{Rotation: 2, FromKeyID: "k2", ToKeyID: "k3", AtSegment: 6},
			{Rotation: 1, FromKeyID: "k1", ToKeyID: "k2", AtSegment: 3},
		},
		Segments: []SegmentResult{
			{SegmentIndex: 2, KeyID: "k1", Status: StatusOK, Reason: reason.NONE},
			{SegmentIndex: 0, KeyID: "k1", Status: StatusOK, Reason: reason.NONE},
			{SegmentIndex: 1, KeyID: "k1", Status: StatusFailed, Reason: reason.AUTH_FAILED, Detail: "gcm"},
		},
	}
}

func TestFinalizeSortsAndCounts(t *testing.T) {
	r := sampleReport()
	r.Finalize()
	if r.SegmentCount != 3 || r.OKCount != 2 || r.FailedCount != 1 {
		t.Fatalf("counts: %d/%d/%d", r.SegmentCount, r.OKCount, r.FailedCount)
	}
	if r.Verdict != StatusFailed || r.ExitCode() != 2 {
		t.Fatalf("verdict %s exit %d, want failed/2", r.Verdict, r.ExitCode())
	}
	for i := 1; i < len(r.Segments); i++ {
		if r.Segments[i-1].SegmentIndex > r.Segments[i].SegmentIndex {
			t.Fatal("segments are not sorted")
		}
	}
	if r.Rotations[0].Rotation != 1 || r.Rotations[1].Rotation != 2 {
		t.Fatal("rotations are not sorted")
	}
}

func TestCleanReportExitsZero(t *testing.T) {
	r := &Report{
		AssetID: "a",
		License: LicenseResult{Status: StatusOK, Reason: reason.NONE},
		Segments: []SegmentResult{
			{SegmentIndex: 0, KeyID: "k1", Status: StatusOK, Reason: reason.NONE},
			{SegmentIndex: 1, KeyID: "k2", Status: StatusOK, Reason: reason.NONE},
		},
	}
	r.Finalize()
	if r.Verdict != StatusOK || r.ExitCode() != 0 {
		t.Fatalf("verdict %s exit %d", r.Verdict, r.ExitCode())
	}
}

func TestJSONIsStableAndOrdered(t *testing.T) {
	a, err := sampleReport().EncodeJSON()
	if err != nil {
		t.Fatal(err)
	}
	b, err := sampleReport().EncodeJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("JSON encoding is not deterministic")
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(a, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"asset_id", "verdict", "rotation_count", "segment_count", "ok_count", "failed_count", "license", "rotations", "segments", "replay", "boundary_check"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing top-level field %q", key)
		}
	}
	// Field order in the serialized struct follows declaration order.
	first := bytes.Index(a, []byte(`"asset_id"`))
	second := bytes.Index(a, []byte(`"verdict"`))
	if first < 0 || second < 0 || first > second {
		t.Fatal("top-level fields are not in declaration order")
	}
}

func TestErrorJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteErrorJSON(&buf, reason.INVALID_INPUT, "bad flag"); err != nil {
		t.Fatal(err)
	}
	var decoded ErrorReport
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Reason != reason.INVALID_INPUT || decoded.Message != "bad flag" {
		t.Fatalf("unexpected error report %+v", decoded)
	}
}

func TestSummaryIsParseable(t *testing.T) {
	var buf bytes.Buffer
	if err := sampleReport().WriteSummary(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"rotation_count=3",
		"segment_count=3",
		"ok_count=2",
		"failed_count=1",
		"segment segment_index=0 key_id=k1 status=ok reason=NONE",
		"segment segment_index=1 key_id=k1 status=failed reason=AUTH_FAILED",
		"rotation index=1 from=k1 to=k2 at_segment=3",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.Contains(line, "=") {
			t.Fatalf("unparseable summary line %q", line)
		}
	}
}

func TestSummaryIncludesOptionalSections(t *testing.T) {
	r := sampleReport()
	r.Replay = ReplayResult{Checked: true, Serial: "lic-1", Attempts: 4, Successes: 1, Replays: 3,
		SuccessReason: reason.NONE, ReplayReason: reason.LICENSE_REPLAY, Rejected: true}
	r.Boundary = BoundaryCheck{Checked: true, AdvancedBy: "75s", LicenseReason: reason.LICENSE_EXPIRED, KeyProbeOK: 0, KeyProbeFailed: 12}
	var buf bytes.Buffer
	if err := r.WriteSummary(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "replay serial=lic-1 attempts=4 successes=1 replays=3") {
		t.Fatalf("missing replay line:\n%s", out)
	}
	if !strings.Contains(out, "boundary advanced_by=75s license_reason=LICENSE_EXPIRED key_probe_ok_count=0 key_probe_failed_count=12") {
		t.Fatalf("missing boundary line:\n%s", out)
	}
}

func TestKeyFactsAreSortedAndRendered(t *testing.T) {
	r := sampleReport()
	r.Keys = []KeyFact{
		{KeyID: "k2", Version: 2, Active: true},
		{KeyID: "k1", Version: 1, Expired: true},
	}
	var buf bytes.Buffer
	if err := r.WriteSummary(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "key key_id=k1 version=1 active=false expired_at_validation=true") {
		t.Fatalf("missing key line:\n%s", out)
	}
	if strings.Index(out, "key_id=k1") > strings.Index(out, "key_id=k2") {
		t.Fatal("key facts are not sorted by version")
	}
}
