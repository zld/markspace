package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vendoredFile resolves the vendored gortsplib file by walking up from the
// test's package directory to the module root (the directory containing
// go.mod), which makes the check independent of the working directory.
func vendoredFile(t *testing.T) (string, bool) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	const rel = "vendor/github.com/bluenviron/gortsplib/v4/server_session_format.go"
	for {
		candidate := filepath.Join(dir, rel)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return "", false // reached module root without the vendor tree
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// TestVendorPatchPresent guards the documented gortsplib vendor modification.
//
// gortsplib's server-side UDP path reorders and de-duplicates packets before
// the application callback, which would hide the gaps and duplicates the
// ingest tool must measure. The vendored readPacketRTPUDP is patched to
// deliver packets in raw arrival order (see vendor/README.md and
// tools/apply-vendor-patch). Running a bare `go mod vendor` restores the
// pristine file; this test then fails with an explicit remediation message
// instead of producing confusing zeroed counters.
func TestVendorPatchPresent(t *testing.T) {
	path, ok := vendoredFile(t)
	if !ok {
		t.Skip("vendor directory not present; patch guard not applicable")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vendored file: %v", err)
	}

	if strings.Contains(string(raw), "[rtspingest vendor patch]") {
		return
	}

	t.Fatalf(
		"gortsplib vendor patch is missing — a bare 'go mod vendor' restored the\n" +
			"pristine file. Re-apply the modification with:\n\n" +
			"    go run -mod=vendor ./tools/apply-vendor-patch\n\n" +
			"Then re-run the tests. See vendor/README.md for details.")
}
