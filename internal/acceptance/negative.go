package acceptance

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediarepair/internal/batch"
	"mediarepair/internal/media"
	"mediarepair/internal/repair"
	"mediarepair/internal/testfixture"
)

func init() {
	RegisterScenario("checksum", runChecksumScenario)
	RegisterScenario("truncated-mp4", runTruncatedMP4Scenario)
	RegisterScenario("truncated_mp4", runTruncatedMP4Scenario)
	RegisterScenario("rtp-ambiguous", func(opts Options, out io.Writer) int {
		return runRTPAmbiguousScenario(out)
	})
	RegisterScenario("rtp_ambiguous", func(opts Options, out io.Writer) int {
		return runRTPAmbiguousScenario(out)
	})
	RegisterScenario("rtp-wrap", func(opts Options, out io.Writer) int {
		return runRTPWrapScenario(out)
	})
	RegisterScenario("rtp_wrap", func(opts Options, out io.Writer) int {
		return runRTPWrapScenario(out)
	})
}

func runChecksumScenario(opts Options, out io.Writer) int {
	if checksumCoverage(opts.Root, out) {
		return ExitInputOrWrite
	}
	return ExitUsage
}

func checksumCoverage(root string, out io.Writer) bool {
	if _, err := testfixture.CreateStandard(root); err != nil {
		PrintError(out, err)
		return false
	}
	framePath := filepath.Join(root, "frames", "frame_0001.png")
	f, err := os.OpenFile(framePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		_, err = f.Write([]byte{0xff})
		_ = f.Close()
	}
	if err != nil {
		fmt.Fprintf(out, "scenario=bad_checksum write_error=%v\n", err)
		return false
	}
	_, err = batch.Run(batch.Options{Root: root, BlockSize: 4})
	fmt.Fprint(out, "scenario=bad_checksum ")
	PrintError(out, err)
	return media.HasCode(err, media.CodeChecksum)
}

func runTruncatedMP4Scenario(opts Options, out io.Writer) int {
	if truncatedMP4Coverage(opts.Root, out) {
		return ExitInputOrWrite
	}
	return ExitUsage
}

func truncatedMP4Coverage(root string, out io.Writer) bool {
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		PrintError(out, err)
		return false
	}
	mp4Path := filepath.Join(root, fx.Manifest.MP4.Path)
	info, err := os.Stat(mp4Path)
	if err != nil {
		fmt.Fprintf(out, "scenario=truncated_mp4 stat_error=%v\n", err)
		return false
	}
	if err := os.Truncate(mp4Path, info.Size()-48); err != nil {
		fmt.Fprintf(out, "scenario=truncated_mp4 truncate_error=%v\n", err)
		return false
	}
	_, err = batch.Run(batch.Options{Root: root, BlockSize: 4})
	fmt.Fprint(out, "scenario=truncated_mp4 ")
	PrintError(out, err)
	return media.HasCode(err, media.CodeMP4Truncated) || media.HasCode(err, media.CodeOffsetOutOfRange)
}

func runRTPAmbiguousScenario(out io.Writer) int {
	if rtpAmbiguousCoverage(out) {
		return ExitInputOrWrite
	}
	return ExitUsage
}

func rtpAmbiguousCoverage(out io.Writer) bool {
	_, err := repair.UnwrapRTPTimestamps([]uint32{100, 100 + 1<<31})
	fmt.Fprint(out, "scenario=rtp_half_period ")
	PrintError(out, err)
	return media.HasCode(err, media.CodeRTPAmbiguous)
}

func runRTPWrapScenario(out io.Writer) int {
	if rtpWrapCoverage(out) {
		return ExitOK
	}
	return ExitUsage
}

func rtpWrapCoverage(out io.Writer) bool {
	got, err := repair.UnwrapRTPTimestamps([]uint32{0xfffffff0, 5, 10})
	if err != nil {
		PrintError(out, err)
		return false
	}
	fmt.Fprintf(out, "scenario=rtp_wrap status=accepted expanded=%v unique=true\n", got)
	return len(got) == 3 && got[1] == 1<<32+5
}
