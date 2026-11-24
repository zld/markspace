package sample

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/clock"
	"mediad/ts"
)

// TestCommittedFixturesParse ensures the checked-in testdata fixtures are
// valid inputs to the TS subsystem and exhibit their documented error model.
func TestCommittedFixturesParse(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata")
	cases := map[string]int{
		"normal.ts":     0,
		"pcr_wrap.ts":   1,
		"cc_jump.ts":    1,
		"psi_update.ts": 1,
	}
	for name, wantErrs := range cases {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Skipf("fixture %s not generated; run scripts/gen-fixtures.sh", name)
		}
		res, fatal := ts.Demux(bytes.NewReader(data), ts.ScanOptions{})
		if fatal != nil {
			t.Fatalf("%s: unexpected fatal error: %v", name, fatal)
		}
		if len(res.Errors) < wantErrs {
			t.Fatalf("%s: want >= %d errors, got %d (%+v)", name, wantErrs, len(res.Errors), res.Errors)
		}
	}

	// The truncated fixture must be fatal with a truncation range error.
	trunc, err := os.ReadFile(filepath.Join(dir, "truncated.ts"))
	if err != nil {
		t.Skip("truncated.ts not generated; run scripts/gen-fixtures.sh")
	}
	_, fatal := ts.Demux(bytes.NewReader(trunc), ts.ScanOptions{})
	var re clock.RangeError
	if fatal == nil {
		t.Fatal("truncated.ts must be fatal")
	}
	if !errors.As(fatal, &re) || re.Kind != clock.KindTruncated {
		t.Fatalf("want truncated range error, got %v", fatal)
	}
}
