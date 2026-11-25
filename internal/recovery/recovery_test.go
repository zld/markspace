package recovery

import (
	"testing"

	"packcheck/internal/segment"
)

func TestIndependentRecoveryAndQuarantine(t *testing.T) {
	m := NewManager()
	m.Register("low", true)
	m.Register("mid", false)
	m.Register("high", true)

	healthy := []segment.LayerSignature{
		{Representation: "low", Timestamps: []uint32{0, 3600, 7200}, Healthy: true},
		{Representation: "high", Timestamps: []uint32{0, 3600, 7200}, Healthy: true},
	}

	// A mid layer whose recomputed boundaries match the shared clock recovers.
	rec := m.Recover("mid", []uint32{0, 3600, 7200}, healthy)
	if rec.Before != VerdictFailed || rec.After != VerdictRecovered || !rec.Healthy {
		t.Errorf("recovery verdict wrong: before=%s after=%s healthy=%v",
			rec.Before, rec.After, rec.Healthy)
	}

	// A second failed layer that cannot match is quarantined.
	m.Register("broken", false)
	q := m.Recover("broken", []uint32{0, 9999}, healthy)
	if q.After != VerdictQuarantined || q.Healthy {
		t.Errorf("quarantine verdict wrong: %+v", q)
	}
	if len(m.Eligible()) != 2 { // only originally-passing low and high
		t.Errorf("eligible=%v", m.Eligible())
	}
}

func TestRecoveryWithoutReferenceQuarantines(t *testing.T) {
	m := NewManager()
	m.Register("lone", false)
	st := m.Recover("lone", []uint32{0, 3600}, nil)
	if st.After != VerdictQuarantined {
		t.Errorf("expected quarantine, got %s", st.After)
	}
	if m.Issues().CountByCode()["truncated-layer"] == 0 {
		t.Error("truncated-layer issue not recorded")
	}
}

func TestManifestExcludesFailedLayersAndIsDeterministic(t *testing.T) {
	eligible := []ManifestEntry{
		{Representation: "mid", Bytes: 200, BoundaryCount: 4, Verdict: VerdictRecovered},
		{Representation: "low", Bytes: 100, BoundaryCount: 4, Verdict: VerdictPassed},
		{Representation: "high", Bytes: 300, BoundaryCount: 4, Verdict: VerdictPassed},
	}
	m1 := BuildManifest(eligible)
	text1, digest1 := m1.Summary()

	// Rebuild from an unsorted copy and assert byte-identical output.
	shuffled := []ManifestEntry{eligible[2], eligible[0], eligible[1]}
	m2 := BuildManifest(shuffled)
	text2, digest2 := m2.Summary()
	if digest1 != digest2 || text1 != text2 {
		t.Errorf("manifest not deterministic:\n%s\n%s", text1, text2)
	}
	if !contains(text1, "entry low") || !contains(text1, "entry mid") || !contains(text1, "entry high") {
		t.Errorf("manifest missing entries: %q", text1)
	}
	// A changed byte count must change the digest.
	mutated := BuildManifest([]ManifestEntry{
		{Representation: "low", Bytes: 101, BoundaryCount: 4, Verdict: VerdictPassed},
		{Representation: "mid", Bytes: 200, BoundaryCount: 4, Verdict: VerdictRecovered},
		{Representation: "high", Bytes: 300, BoundaryCount: 4, Verdict: VerdictPassed},
	})
	_, digest3 := mutated.Summary()
	if digest3 == digest1 {
		t.Error("content change did not alter manifest digest")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
