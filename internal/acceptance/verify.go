package acceptance

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"continuity/internal/fixture"
	"continuity/internal/manifest"
	"continuity/internal/media"
	"continuity/internal/pipeline"
	rtprec "continuity/internal/rtp"
)

// Verify runs the terminal acceptance scenarios. Returned errors mean acceptance failed.
func Verify(caseName, out string) error {
	if out == "" {
		out = filepath.Join("/tmp", "continuity-"+caseName)
	}
	switch caseName {
	case "clean":
		return verifyClean(out)
	case "damaged":
		return verifyDamaged(out)
	case "boundary":
		return verifyBoundary(out)
	case "recovery":
		return verifyRecovery(out)
	default:
		return fmt.Errorf("unknown verify case %q", caseName)
	}
}

func verifyClean(out string) error {
	_ = os.RemoveAll(out)
	res, err := pipeline.Run(pipeline.Options{Case: "clean", Out: out})
	if err != nil {
		return err
	}
	for _, line := range pipeline.MetricLines(res) {
		fmt.Println(line)
	}
	if res.Timeline == nil || res.Timeline.SyncMaxNS > 20_000_000 {
		return fmt.Errorf("sync_max_us exceeds 20000")
	}
	if res.RTP == nil || res.RTP.Recovered != 4 || res.RTP.RecoveryRate != 100 {
		return fmt.Errorf("RTP recovery does not meet 4/4 100%%")
	}
	if res.Render == nil || len(res.Render.Frames) != 8 {
		return fmt.Errorf("expected 8 frames")
	}
	if res.Render.P95MS > 1000 {
		return fmt.Errorf("p95_ms exceeds 1000")
	}
	fmt.Println("verify=clean result=pass")
	return nil
}

func verifyDamaged(out string) error {
	_ = os.RemoveAll(out)
	root := filepath.Join(out, "sample")
	spec, err := fixture.Build("damaged", root)
	if err != nil {
		return err
	}
	_, oobErr := manifest.ParseHLSPlaylist(spec.Entry, root, "video")
	oobFault, ok := media.AsFault(oobErr)
	if !ok || oobFault.Code != media.CodePathEscape {
		return fmt.Errorf("expected OOB path rejection, got %v", oobErr)
	}
	fmt.Printf("oob_reference=%s\n", spec.OOBReference)
	fmt.Printf("out_of_bounds_rejected=true location=%s:%d\n", oobFault.Source, oobFault.Line)

	_, truncErr := media.ReadFragment(spec.TruncatedFile, spec.VideoTimescale)
	tf, ok := media.AsFault(truncErr)
	if !ok || tf.Code != media.CodeTruncatedMP4 {
		return fmt.Errorf("expected truncation fault, got %v", truncErr)
	}
	fmt.Printf("truncation_offset=%d\n", tf.Offset)
	fmt.Printf("truncation_detail=%q\n", tf.Detail)

	code, combined, err := runBinary("run", "--case", "damaged", "--out", out)
	if err != nil {
		return fmt.Errorf("failed to launch direct input run: %w output=%s", err, combined)
	}
	if code != 2 {
		return fmt.Errorf("direct violating run exit=%d, want 2; output=%s", code, combined)
	}
	fmt.Println("direct_input_exit=2")
	if err := assertNoPublication(out); err != nil {
		return err
	}
	fmt.Println("accepted=false")
	fmt.Println("published_artifacts=0")
	fmt.Println("verify=damaged result=pass expected_faults=path_escape,truncated_mp4")
	return nil
}

func verifyBoundary(out string) error {
	_ = os.RemoveAll(out)
	root := filepath.Join(out, "sample")
	spec, err := fixture.Build("boundary", root)
	if err != nil {
		return err
	}
	_, dashErr := manifest.ParseDASH(spec.Entry, root)
	of, ok := media.AsFault(dashErr)
	if !ok || of.Code != media.CodeTimeOverflow {
		return fmt.Errorf("expected time overflow, got %v", dashErr)
	}
	fmt.Printf("time_overflow=rejected time_overflow_offset=%d\n", of.Offset)

	pkts, err := rtprec.ReadRecords(spec.RTPPath)
	if err != nil {
		return err
	}
	rep := rtprec.RecoverPackets(pkts)
	if rep.Unrecoverable != 2 {
		return fmt.Errorf("unrecoverable=%d want 2", rep.Unrecoverable)
	}
	fmt.Printf("unrecoverable_packets=%d gaps=%d\n", rep.Unrecoverable, len(rep.Gaps))

	code, combined, err := runBinary("run", "--case", "boundary", "--out", out)
	if err != nil {
		return fmt.Errorf("failed to launch boundary run: %w output=%s", err, combined)
	}
	if code != 2 {
		return fmt.Errorf("boundary run exit=%d want 2; output=%s", code, combined)
	}
	fmt.Println("direct_input_exit=2")
	if err := assertNoPublication(out); err != nil {
		return err
	}
	fmt.Println("published_artifacts=0")
	fmt.Println("verify=boundary result=pass expected_faults=time_overflow,unrecoverable_rtp")
	return nil
}

func verifyRecovery(out string) error {
	_ = os.RemoveAll(out)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	first := exec.Command(exe, "run", "--case", "recovery", "--out", out, "--inject-crash")
	var firstBuf bytes.Buffer
	first.Stdout = &firstBuf
	first.Stderr = &firstBuf
	if err := first.Run(); err == nil {
		return fmt.Errorf("first recovery run unexpectedly succeeded")
	} else if code := exitCode(err); code != 75 {
		return fmt.Errorf("first recovery run exit=%d want75 output=%s", code, firstBuf.String())
	}
	frames, _ := filepath.Glob(filepath.Join(out, "frames", "*.png"))
	if len(frames) != 8 {
		return fmt.Errorf("after crash frames=%d want8", len(frames))
	}
	if _, err := os.Stat(filepath.Join(out, "SHA256SUMS")); err != nil {
		return fmt.Errorf("checksum summary missing after crash: %w", err)
	}
	if _, err := os.Stat(filepath.Join(out, "state.json")); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("state.json must be absent after injected crash")
	}
	fmt.Println("first_subprocess_exit=75")
	fmt.Println("artifacts_persisted=9 state_committed=0")

	second := exec.Command(exe, "run", "--case", "recovery", "--out", out)
	var outBuf, errBuf bytes.Buffer
	second.Stdout = &outBuf
	second.Stderr = &errBuf
	if err := second.Run(); err != nil {
		return fmt.Errorf("second recovery run failed: %w stdout=%s stderr=%s", err, outBuf.String(), errBuf.String())
	}
	fmt.Print(outBuf.String())
	output := outBuf.String()
	for _, want := range []string{"recovered_artifacts=1", "published=8", "duplicates=0", "rollback_checkpoint=1"} {
		if !strings.Contains(output, want) {
			return fmt.Errorf("second run output missing %q; full=%s err=%s", want, output, errBuf.String())
		}
	}
	fmt.Println("verify=recovery result=pass first_exit=75 second_exit=0")
	return nil
}

func runBinary(args ...string) (int, string, error) {
	exe, err := os.Executable()
	if err != nil {
		return -1, "", err
	}
	cmd := exec.Command(exe, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	if err == nil {
		return 0, buf.String(), nil
	}
	return exitCode(err), buf.String(), nil
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func assertNoPublication(out string) error {
	if _, err := os.Stat(filepath.Join(out, "state.json")); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("state.json was published for invalid input")
	}
	frames, err := filepath.Glob(filepath.Join(out, "frames", "*.png"))
	if err != nil {
		return err
	}
	if len(frames) != 0 {
		return fmt.Errorf("%d frames published for invalid input", len(frames))
	}
	return nil
}
