package pipeline

import "testing"

func TestSimulateCrashRecoveryHash(t *testing.T) {
	root := t.TempDir()
	report, err := SimulateCrashRecovery(
		root+"/crash", root+"/reference", 42, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Recovered {
		t.Fatalf("recovered hash=%s != full rerun=%s",
			report.RecoverHash, report.FullHash)
	}
	if report.FullHash == "" || len(report.FullHash) != 16 {
		t.Fatalf("malformed commit hash: %q", report.FullHash)
	}
}
