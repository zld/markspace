package report

import "testing"

func TestCanonicalHashStableAndOrderIndependent(t *testing.T) {
	a := map[string][]byte{"x": {1, 2}, "y": {3, 4}, "z": {5}}
	b := map[string][]byte{"z": {5}, "x": {1, 2}, "y": {3, 4}}
	ha, hb := CanonicalHash(a), CanonicalHash(b)
	if ha != hb {
		t.Fatal("hash must not depend on map iteration order")
	}
	if err := CheckHexSHA256(ha); err != nil {
		t.Fatal(err)
	}
	c := map[string][]byte{"x": {1, 2}, "y": {3, 4}, "z": {6}}
	if CanonicalHash(c) == ha {
		t.Fatal("different content must hash differently")
	}
}

func TestReportTextFields(t *testing.T) {
	r := Report{
		CaseCount: 3, FaultCount: 5, EarliestBadPacket: 1000, LocateErrorNS: 123,
		RecoveryMS: 42, RollbackStage: "audio-normalize", CommitBoundary: "sync-align",
		SHA256: "a", RepeatSHAEqual: true,
	}
	txt := r.Text()
	for _, want := range []string{
		"case_count=3", "fault_count=5", "earliest_bad_packet=1000",
		"locate_error_ns=123", "recovery_ms=42", "rollback_stage=audio-normalize",
		"commit_boundary=sync-align", "repeat_sha256_equal=true",
	} {
		if !contains(txt, want) {
			t.Errorf("output missing %q:\n%s", want, txt)
		}
	}
}

func TestCheckHexSHA256(t *testing.T) {
	good := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := CheckHexSHA256(good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"short", "g" + good[1:], good + "00"} {
		if err := CheckHexSHA256(bad); err == nil {
			t.Errorf("bad digest %q accepted", bad)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
