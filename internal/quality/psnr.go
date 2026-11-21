// Package quality computes frame-level PSNR between a reference range
// of the source and a decoded output segment. Frames are streamed one
// at a time so no whole video is ever resident in memory.
package quality

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
)

// maxPSNR caps the value reported for identical frames (MSE == 0) so
// aggregates stay finite.
const maxPSNR = 99.0

// Stats aggregates per-frame PSNR for one segment.
type Stats struct {
	Frames      int     `json:"frames"`
	MeanDB      float64 `json:"mean_db"`
	MinDB       float64 `json:"min_db"`
	MaxSwingDB  float64 `json:"max_swing_db"` // max |psnr[i]-psnr[i-1]|
	BelowFloor  int     `json:"below_floor"`  // frames under the configured threshold
	FrameMismatch bool  `json:"frame_mismatch"`
}

// Range computes per-frame luma PSNR between refPath[start, start+dur)
// and the already-trimmed decPath. A frame-count mismatch is reported
// in Stats.FrameMismatch and no quality values are fabricated: MeanDB
// is left at zero and the caller must treat the segment as failed.
func Range(ctx context.Context, refPath, decPath string, start, dur float64, width, height int, floorDB float64) (*Stats, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid geometry %dx%d", width, height)
	}
	frameSize := width * height * 3 / 2
	lumaSize := width * height
	base := []string{"-v", "error", "-nostdin"}
	refArgs := append(append([]string{}, base...),
		"-ss", strconv.FormatFloat(start, 'f', 6, 64),
		"-t", strconv.FormatFloat(dur, 'f', 6, 64),
		"-i", refPath,
		"-map", "0:v:0", "-vf", fmt.Sprintf("scale=%d:%d", width, height),
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "pipe:1")
	decArgs := append(append([]string{}, base...),
		"-i", decPath,
		"-map", "0:v:0", "-vf", fmt.Sprintf("scale=%d:%d", width, height),
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "pipe:1")

	refCmd := exec.CommandContext(ctx, "ffmpeg", refArgs...)
	refPipe, err := refCmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := refCmd.Start(); err != nil {
		return nil, fmt.Errorf("decode reference: %w", err)
	}
	decCmd := exec.CommandContext(ctx, "ffmpeg", decArgs...)
	decPipe, err := decCmd.StdoutPipe()
	if err != nil {
		refCmd.Cancel()
		return nil, err
	}
	if err := decCmd.Start(); err != nil {
		refCmd.Cancel()
		return nil, fmt.Errorf("decode segment: %w", err)
	}
	refCh := make(chan []byte, 2)
	decCh := make(chan []byte, 2)
	go streamFrames(refPipe, frameSize, refCh)
	go streamFrames(decPipe, frameSize, decCh)

	st := &Stats{MinDB: maxPSNR}
	var sum, prev float64
	for {
		rf, rok := <-refCh
		df, dok := <-decCh
		if !rok || !dok {
			if rok != dok {
				st.FrameMismatch = true
			}
			break
		}
		p := framePSNR(rf[:lumaSize], df[:lumaSize])
		if st.Frames > 0 {
			if sw := math.Abs(p - prev); sw > st.MaxSwingDB {
				st.MaxSwingDB = sw
			}
		}
		prev = p
		sum += p
		st.Frames++
		if p < st.MinDB {
			st.MinDB = p
		}
		if p < floorDB {
			st.BelowFloor++
		}
	}
	refCmd.Wait()
	decCmd.Wait()
	if st.Frames == 0 && !st.FrameMismatch {
		return nil, errors.New("no frames decoded from either input")
	}
	if st.FrameMismatch {
		return st, nil // caller treats mismatch as segment failure
	}
	st.MeanDB = sum / float64(st.Frames)
	return st, nil
}

// streamFrames copies frameSize-byte frames from pipe into ch.
func streamFrames(pipe io.Reader, frameSize int, ch chan<- []byte) {
	defer close(ch)
	buf := make([]byte, frameSize)
	for {
		if _, err := io.ReadFull(pipe, buf); err != nil {
			return
		}
		frame := make([]byte, frameSize)
		copy(frame, buf)
		ch <- frame
	}
}

// framePSNR computes PSNR in dB between two equal-length luma planes.
func framePSNR(a, b []byte) float64 {
	var acc float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		acc += d * d
	}
	mse := acc / float64(len(a))
	if mse == 0 {
		return maxPSNR
	}
	p := 10 * math.Log10(255*255/mse)
	if p > maxPSNR {
		return maxPSNR
	}
	return p
}
