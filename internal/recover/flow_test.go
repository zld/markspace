package recover_test

import (
	"errors"
	"os"
	"testing"

	"mediapack/internal/commit"
	"mediapack/internal/fanout"
	"mediapack/internal/manifest"
	"mediapack/internal/recover"
)

// TestCrashBeforeCommitRecoverAndVerify mirrors the documented acceptance run:
// demo --crash-before-commit -> crash sentinel(20) -> recover 1/0/0 -> verify.
func TestCrashBeforeCommitRecoverAndVerify(t *testing.T) {
	dir := t.TempDir()
	_, err := recover.Demo(recover.Options{WorkDir: dir, Rates: 2, CrashBeforeCommit: true})
	if !errors.Is(err, commit.ErrSimulatedCrash) {
		t.Fatalf("want simulated crash, got %v", err)
	}

	res, _, err := recover.Recover(dir)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if res.Recovered != 1 || res.DuplicatePublish != 0 || res.MissingArtifact != 0 {
		t.Fatalf("recover counters=%+v want recovered=1 dup=0 missing=0", res)
	}
	if res.AdoptedTemp != 1 || res.NewlyPublished != 7 {
		t.Fatalf("adoption/publish breakdown=%+v", res)
	}

	rep, m, err := recover.Verify(dir)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.Pass {
		t.Fatalf("verify report not passing: %+v", rep)
	}
	if rep.RoundtripRate != 1.0 || rep.KeyframeRate != 1.0 {
		t.Fatalf("rates=%v/%v want 1.000", rep.RoundtripRate, rep.KeyframeRate)
	}
	if rep.HashOK != rep.HashTotal || rep.HashTotal != 8 {
		t.Fatalf("segment hashes %d/%d want 8/8", rep.HashOK, rep.HashTotal)
	}
	if rep.FragmentChecksOK != rep.FragmentTotal {
		t.Fatalf("fragment structural checks %d/%d", rep.FragmentChecksOK, rep.FragmentTotal)
	}
	if rep.BoundaryPackets > 1 || !rep.Aligned {
		t.Fatalf("boundary packets=%d aligned=%v", rep.BoundaryPackets, rep.Aligned)
	}

	// Independent per-fragment hash re-verification from disk.
	for _, a := range m.Artifacts {
		full, err := manifest.Resolve(dir, a.RelPath)
		if err != nil {
			t.Fatal(err)
		}
		sum, n, err := manifest.HashFile(full)
		if err != nil {
			t.Fatal(err)
		}
		if sum != a.SHA256 || n != a.Length {
			t.Fatalf("independent hash mismatch for %s", a.ID)
		}
		b, _ := os.ReadFile(full)
		if _, err := fanout.Check(b); err != nil {
			t.Fatalf("fragment %s structural check: %v", a.ID, err)
		}
	}
}

// TestCorruptOneFragmentRebuildsOnlyIt verifies local recovery scope: the
// damaged fragment is rebuilt byte-identical and every other hash is unchanged.
func TestCorruptOneFragmentRebuildsOnlyIt(t *testing.T) {
	dir := t.TempDir()
	if _, err := recover.Demo(recover.Options{WorkDir: dir, Rates: 2}); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, a := range m.Artifacts {
		full, _ := manifest.Resolve(dir, a.RelPath)
		sum, _, _ := manifest.HashFile(full)
		before[a.ID] = sum
	}

	target := m.Artifacts[3]
	full, _ := manifest.Resolve(dir, target.RelPath)
	b, _ := os.ReadFile(full)
	b[len(b)/2] ^= 0xFF
	if err := os.WriteFile(full, b, 0o644); err != nil {
		t.Fatal(err)
	}

	res, _, err := recover.Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 1 || res.Rebuilt != 1 {
		t.Fatalf("want exactly one rebuild, got %+v", res)
	}

	m2, _ := manifest.Load(dir)
	for _, a := range m2.Artifacts {
		f, _ := manifest.Resolve(dir, a.RelPath)
		sum, _, _ := manifest.HashFile(f)
		if sum != a.SHA256 {
			t.Fatalf("rebuilt artifact %s hash not restored", a.ID)
		}
		if a.ID == target.ID {
			if sum != before[a.ID] {
				t.Fatal("rebuilt fragment is not byte-identical to the original")
			}
			continue
		}
		if sum != before[a.ID] {
			t.Fatalf("unaffected fragment %s hash changed", a.ID)
		}
	}

	rep, _, _ := recover.Verify(dir)
	if !rep.Pass || rep.HashOK != rep.HashTotal {
		t.Fatalf("verify after corruption recovery failed: %+v", rep)
	}
}

// TestSecondRecoverIsNoop ensures re-running recovery without damage neither
// republishes nor changes counters incorrectly.
func TestSecondRecoverIsNoop(t *testing.T) {
	dir := t.TempDir()
	if _, err := recover.Demo(recover.Options{WorkDir: dir, Rates: 2, CrashBeforeCommit: true}); err == nil {
		t.Fatal("want crash sentinel")
	}
	if _, _, err := recover.Recover(dir); err != nil {
		t.Fatal(err)
	}
	res, _, err := recover.Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 0 || res.NewlyPublished != 0 || res.AlreadyConsistent != 8 {
		t.Fatalf("second recover should be a no-op: %+v", res)
	}
}
