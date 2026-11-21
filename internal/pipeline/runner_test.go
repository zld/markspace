package pipeline_test

import (
	"strings"
	"testing"
	"time"

	"mediaseal/internal/fixture"
	"mediaseal/internal/license"
	"mediaseal/internal/media"
	"mediaseal/internal/pipeline"
	"mediaseal/internal/report"
)

func testClock() *media.ManualClock {
	return media.NewManualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}

func emptyInjection() pipeline.Injection {
	return pipeline.Injection{
		Tamper:         map[int]bool{},
		Replay:         map[int]bool{},
		ExpiredKey:     map[int]bool{},
		ExpiredLicense: map[int]bool{},
	}
}

func runConfig(t *testing.T, cfg pipeline.Config) *pipeline.Result {
	t.Helper()
	res, err := pipeline.Run(cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// TestDemoAcceptance mirrors the documented demo invocation and pins its
// aggregate counts.
func TestDemoAcceptance(t *testing.T) {
	inj := emptyInjection()
	inj.Tamper[2] = true
	inj.Replay[4] = true
	inj.ExpiredKey[7] = true
	inj.ExpiredLicense[10] = true
	res := runConfig(t, pipeline.Config{
		AssetID: "demo-asset", Segments: 12, RotateEvery: 3, Inject: inj, Clock: testClock(),
	})
	s := report.Build(res)
	if s.RotationCount != 3 || s.KeyCount != 4 {
		t.Errorf("rotation_count=%d key_count=%d, want 3 and 4", s.RotationCount, s.KeyCount)
	}
	if s.RecordCount != 13 || s.AcceptedCount != 7 || s.RejectedCount != 6 {
		t.Errorf("record=%d accepted=%d rejected=%d, want 13/7/6",
			s.RecordCount, s.AcceptedCount, s.RejectedCount)
	}
	want := map[string]int{
		media.ReasonKeyExpired:     3,
		media.ReasonAuthFailed:     1,
		media.ReasonReplay:         1,
		media.ReasonLicenseExpired: 1,
	}
	for reason, count := range want {
		if s.FailureCounts[reason] != count {
			t.Errorf("failure_counts[%q]=%d, want %d", reason, s.FailureCounts[reason], count)
		}
	}
	for _, rec := range s.Records {
		if rec.KeyID == "" {
			t.Errorf("record %s has empty key_id", rec.SegmentID)
		}
		if rec.Status == media.StatusAccepted && rec.FailureReason != "" {
			t.Errorf("accepted record %s has failure reason %q", rec.SegmentID, rec.FailureReason)
		}
	}
}

// TestReplayDeterministic submits the same segment twice and requires the
// first request to pass and the duplicate to fail, identically across runs.
func TestReplayDeterministic(t *testing.T) {
	var prev []media.Record
	for run := 0; run < 3; run++ {
		inj := emptyInjection()
		inj.Replay[1] = true
		res := runConfig(t, pipeline.Config{
			AssetID: "asset", Segments: 4, RotateEvery: 2, Inject: inj, Clock: testClock(),
		})
		if len(res.Records) != 5 {
			t.Fatalf("run %d: got %d records, want 5", run, len(res.Records))
		}
		first, dup := res.Records[1], res.Records[2]
		if first.SegmentID != dup.SegmentID {
			t.Fatalf("run %d: replay pair misaligned: %q vs %q", run, first.SegmentID, dup.SegmentID)
		}
		if first.Status != media.StatusAccepted {
			t.Errorf("run %d: first request = %s, want accepted", run, first.Status)
		}
		if dup.Status != media.StatusRejected || dup.FailureReason != media.ReasonReplay {
			t.Errorf("run %d: duplicate = %s/%q, want rejected/replay_detected",
				run, dup.Status, dup.FailureReason)
		}
		if prev != nil {
			for i := range prev {
				if prev[i] != res.Records[i] {
					t.Fatalf("run %d: record %d differs across runs: %+v vs %+v",
						run, i, prev[i], res.Records[i])
				}
			}
		}
		prev = res.Records
	}
}

// TestTamperFailsAuth flips one ciphertext bit and requires GCM
// authentication to fail.
func TestTamperFailsAuth(t *testing.T) {
	inj := emptyInjection()
	inj.Tamper[0] = true
	res := runConfig(t, pipeline.Config{
		AssetID: "asset", Segments: 2, RotateEvery: 1, Inject: inj, Clock: testClock(),
	})
	rec := res.Records[0]
	if rec.Status != media.StatusRejected || rec.FailureReason != media.ReasonAuthFailed {
		t.Errorf("tampered segment = %s/%q, want rejected/ciphertext_auth_failed",
			rec.Status, rec.FailureReason)
	}
	if res.Records[1].Status != media.StatusAccepted {
		t.Errorf("untampered segment = %s, want accepted", res.Records[1].Status)
	}
}

// TestExpiredKeyAffectsWholeEpoch expires one key and requires every
// segment of that epoch to fail with key_expired.
func TestExpiredKeyAffectsWholeEpoch(t *testing.T) {
	inj := emptyInjection()
	inj.ExpiredKey[2] = true // epoch 1 covers segments 2 and 3
	res := runConfig(t, pipeline.Config{
		AssetID: "asset", Segments: 6, RotateEvery: 2, Inject: inj, Clock: testClock(),
	})
	for i, rec := range res.Records {
		switch {
		case i == 2 || i == 3:
			if rec.Status != media.StatusRejected || rec.FailureReason != media.ReasonKeyExpired {
				t.Errorf("segment %d = %s/%q, want rejected/key_expired",
					i, rec.Status, rec.FailureReason)
			}
		default:
			if rec.Status != media.StatusAccepted {
				t.Errorf("segment %d = %s/%q, want accepted", i, rec.Status, rec.FailureReason)
			}
		}
	}
}

// TestExpiredLicense requires an out-of-window license to be reported.
func TestExpiredLicense(t *testing.T) {
	inj := emptyInjection()
	inj.ExpiredLicense[1] = true
	res := runConfig(t, pipeline.Config{
		AssetID: "asset", Segments: 3, RotateEvery: 3, Inject: inj, Clock: testClock(),
	})
	rec := res.Records[1]
	if rec.Status != media.StatusRejected || rec.FailureReason != media.ReasonLicenseExpired {
		t.Errorf("segment 1 = %s/%q, want rejected/license_expired", rec.Status, rec.FailureReason)
	}
}

// TestLicenseValidatorFailures exercises signature and segment-binding
// failures directly against the issuer.
func TestLicenseValidatorFailures(t *testing.T) {
	clock := testClock()
	issuer, err := license.NewIssuer(clock)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	segs := fixture.Generate("asset", 2)
	lic, err := issuer.Issue(segs[0], "key-0000")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if reason := issuer.Validate(lic, segs[0]); reason != media.ReasonNone {
		t.Fatalf("valid license rejected: %q", reason)
	}
	bad := lic
	bad.Signature = append([]byte{}, lic.Signature...)
	bad.Signature[0] ^= 0xFF
	if reason := issuer.Validate(bad, segs[0]); reason != media.ReasonLicenseSignature {
		t.Errorf("corrupted signature = %q, want license_invalid_signature", reason)
	}
	if reason := issuer.Validate(lic, segs[1]); reason != media.ReasonLicenseSegment {
		t.Errorf("wrong segment = %q, want license_segment_mismatch", reason)
	}
}

// TestOutputOrderUnderConcurrency processes enough segments to saturate the
// worker pool and requires records to stay in input order.
func TestOutputOrderUnderConcurrency(t *testing.T) {
	res := runConfig(t, pipeline.Config{
		AssetID: "asset", Segments: 40, RotateEvery: 3, Inject: emptyInjection(), Clock: testClock(),
	})
	if len(res.Records) != 40 {
		t.Fatalf("got %d records, want 40", len(res.Records))
	}
	segs := fixture.Generate("asset", 40)
	for i, rec := range res.Records {
		if rec.SegmentID != segs[i].ID() {
			t.Fatalf("record %d = %q, want %q", i, rec.SegmentID, segs[i].ID())
		}
		if rec.Status != media.StatusAccepted {
			t.Errorf("record %d = %s/%q, want accepted", i, rec.Status, rec.FailureReason)
		}
	}
}

// TestReportContainsNoSecrets ensures the rendered report never carries key
// material, private keys, signatures or license nonces.
func TestReportContainsNoSecrets(t *testing.T) {
	res := runConfig(t, pipeline.Config{
		AssetID: "asset", Segments: 4, RotateEvery: 2, Inject: emptyInjection(), Clock: testClock(),
	})
	var buf strings.Builder
	if err := report.WriteJSON(&buf, report.Build(res)); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	out := buf.String()
	for _, forbidden := range []string{"nonce", "signature", "private", "material"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("report contains forbidden field %q", forbidden)
		}
	}
}
