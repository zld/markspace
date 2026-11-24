package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"mediasync/internal/fixture"
	"mediasync/internal/model"
	"mediasync/internal/mp4"
	"mediasync/internal/output"
)

// Verify inspects a fixture that contains a deliberately damaged MP4 and
// reports the byte offset where damage was located. Exit code 2 means the
// input is damaged but the location is within one byte of the injection point.
func Verify(args []string) (int, error) {
	fs := newFlagSet("verify", os.Stderr)
	name := fs.String("fixture", "", "fixture name: corrupt-truncated, edit-out-of-range, or a path to an .mp4")
	expect := fs.Int64("expect-offset", 4096, "byte offset where damage was injected")
	workdir := fs.String("workdir", "", "working directory (default: ./mediasync-work)")
	if err := parseArgs(fs, args); err != nil {
		return output.ExitFailure, err
	}
	wd, err := resolveWorkDir(*workdir)
	if err != nil {
		return output.ExitFailure, err
	}
	path, injectAt, err := fixture.Materialize(*name, wd)
	if err != nil {
		return output.ExitFailure, err
	}
	info, ierr := mp4.InspectFile(path)
	sum := output.Summary{Command: "verify", Fixture: *name, ExpectedOffset: *expect}
	if ierr != nil && (info == nil || len(info.Diagnostics) == 0) {
		sum.OK = false
		sum.ExitCode = output.ExitFailure
		sum.ErrorCount = 1
		sum.Messages = []string{ierr.Error()}
		_ = printSummary(os.Stdout, sum)
		return output.ExitFailure, ierr
	}
	var diags model.ErrorList
	if info != nil {
		diags = info.Diagnostics
	}
	sum.ErrorCount = len(diags)
	located := int64(-1)
	if len(diags) > 0 {
		located = diags[0].Loc.ByteOffset
	}
	want := *expect
	if injectAt > 0 {
		want = injectAt
	}
	sum.LocatedOffset = located
	sum.ExpectedOffset = want
	// An intact file is a success: there is no damage to locate.
	if len(diags) == 0 {
		sum.OK = true
		sum.ExitCode = output.ExitOK
		sum.Messages = []string{"no structural damage found"}
		if err := printSummary(os.Stdout, sum); err != nil {
			return output.ExitFailure, err
		}
		fmt.Fprintf(os.Stderr, "verify: %s is intact\n", path)
		return output.ExitOK, nil
	}
	code, dev := output.VerifyTruncation(located, want, len(diags))
	sum.OffsetDeviation = dev
	sum.OK = code == output.ExitCorrupted
	sum.ExitCode = code
	sum.Messages = errorMessages(diags)
	sum.Digest = diagsDigest(diags)
	if err := printSummary(os.Stdout, sum); err != nil {
		return output.ExitFailure, err
	}
	fmt.Fprintf(os.Stderr, "verify: %s damage located at %d (expected %d, deviation %d)\n",
		path, located, want, dev)
	return code, nil
}

func diagsDigest(errs model.ErrorList) string {
	// Hash the diagnostics only; file paths are already fixed by the workdir.
	h := sha256Hex(errs.Error())
	return h
}

var _ = filepath.Join
