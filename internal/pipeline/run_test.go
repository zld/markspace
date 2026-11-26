package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"replayctl/internal/fault"
	"replayctl/internal/fixture"
	"replayctl/internal/journal"
)

func testInput(t *testing.T, caseName string, crash bool) (Input, *fixture.Paths) {
	t.Helper()
	p, err := fixture.GenerateSelf(fixture.Options{Root: t.TempDir(), Case: caseName})
	if err != nil {
		t.Fatal(err)
	}
	return Input{
		Out:              t.TempDir(),
		MediaPath:        p.MediaPath,
		EventPath:        p.EventPath,
		EventID:          p.EventID,
		StartMS:          500,
		EndMS:            1500,
		Timeline:         p.Timeline,
		CrashAfterRename: crash,
	}, p
}

func TestRenameCrashRecoveryCounters(t *testing.T) {
	in, paths := testInput(t, fixture.CaseRecover, true)

	_, err := Run(in)
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.InjectedExit || fault.ExitStatus(err) != 75 {
		t.Fatalf("first err=%v, want injected exit 75", err)
	}
	key := journal.DeliveryKey(paths.EventID, 500, 1500)
	store, err := journal.Open(in.Out)
	if err != nil {
		t.Fatal(err)
	}
	final, err := store.FinalPath(key)
	if err != nil {
		t.Fatal(err)
	}
	if !fileExists(final) {
		t.Fatal("renamed artifact missing after injected crash")
	}
	recs, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := recs[key]; ok {
		t.Fatal("journal must not be committed before the injected crash point")
	}

	second, err := Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if second.Recovered != 1 || second.Published != 1 || second.Duplicate != 0 {
		t.Fatalf("second counters=%+v", second)
	}
	third, err := Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if third.Recovered != 0 || third.Published != 1 || third.Duplicate != 0 {
		t.Fatalf("third counters=%+v", third)
	}
	if second.SHA256 != third.SHA256 || len(second.SHA256) != 64 {
		t.Fatalf("hash mismatch/length: %q %q", second.SHA256, third.SHA256)
	}
	verified, metrics, err := Verify(in.Out)
	if err != nil {
		t.Fatal(err)
	}
	if verified.SHA256 != second.SHA256 || metrics.Frames != 25 || metrics.Pixels != 6400 {
		t.Fatalf("verify mismatch: %+v %+v", verified, metrics)
	}
	recs, _ = store.Load()
	if len(recs) != 1 {
		t.Fatalf("journal records=%d want exactly one", len(recs))
	}

	if err := os.Remove(filepath.Join(in.Out, journal.ManifestName)); err != nil {
		t.Fatal(err)
	}
	fourth, err := Run(in)
	if err != nil || fourth.Recovered != 0 || fourth.Published != 1 {
		t.Fatalf("manifest regeneration result=%+v err=%v", fourth, err)
	}
	if _, err := os.Stat(filepath.Join(in.Out, journal.ManifestName)); err != nil {
		t.Fatalf("manifest was not regenerated: %v", err)
	}
}

func TestTimeGapRetainsEventOffset(t *testing.T) {
	in, _ := testInput(t, fixture.CaseTimeGap, false)
	_, err := Run(in)
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.TimeGap {
		t.Fatalf("err=%v want TIME_GAP", err)
	}
	if fe.Offset < 0 || fe.Path == "" {
		t.Fatalf("time gap fault missing raw offset/path: %+v", fe)
	}
	if fault.ExitStatus(err) != 2 {
		t.Fatalf("exit=%d want2", fault.ExitStatus(err))
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
