package verify

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"regress/internal/fault"
	"regress/internal/fixture"
)

var ssimAllRE = regexp.MustCompile(`All:([0-9.]+)`)

// MeasureSSIM independently recomputes mean SSIM between a candidate encoded
// file and the reference source. The verifier invokes FFmpeg itself instead
// of reading the planner's number, so a faulty planner cannot self-approve.
// Geometry is normalized before comparison to support lower-resolution rungs.
func MeasureSSIM(ctx context.Context, codec, candidatePath, sourcePath string,
	sourceW, sourceH, fps int) (float64, error) {
	bin, err := fixture.FFMpegPath()
	if err != nil {
		return 0, err
	}
	filter := fmt.Sprintf(
		"[0:v]fps=%d,scale=%d:%d:flags=bicubic[dist];[1:v]fps=%d[ref];[dist][ref]ssim=stats_file=-",
		fps, sourceW, sourceH, fps)
	args := []string{
		"-hide_banner", "-nostdin",
		"-i", candidatePath, "-i", sourcePath,
		"-lavfi", filter, "-f", "null", "-",
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, fault.New(fault.StageVerify, fault.CodeSubprocessTimeout, codec,
				"independent ssim timed out").Wrap(err)
		}
		return 0, fault.New(fault.StageVerify, fault.CodeSubprocessFailed, codec,
			"independent ssim failed: "+out.String()).Wrap(err)
	}
	return parseSSIM(out.String())
}

func parseSSIM(log string) (float64, error) {
	idx := strings.LastIndex(log, "SSIM")
	segment := log
	if idx >= 0 {
		segment = log[idx:]
	}
	m := ssimAllRE.FindStringSubmatch(segment)
	if m == nil {
		return 0, fault.New(fault.StageVerify, fault.CodeSubprocessFailed, "",
			"independent ssim aggregate not found in filter output")
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fault.New(fault.StageVerify, fault.CodeSubprocessFailed, "",
			"independent ssim parse failed").Wrap(err)
	}
	if v > 1.0 {
		v /= 100
	}
	return v, nil
}
