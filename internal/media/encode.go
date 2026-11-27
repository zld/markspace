package media

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"mediacore/internal/fault"
)

// FineTunePass records one encoder attempt in the bitrate feedback loop.
type FineTunePass struct {
	TargetEncKbps float64 `json:"target_enc_kbps"`
	Bytes         int64   `json:"bytes"`
	Kbps          float64 `json:"kbps"`
}

// FineTune records the feedback iterations used to reach the target rate.
type FineTune struct {
	Iterations int            `json:"iterations"`
	Passes     []FineTunePass `json:"passes"`
}

// rateTolerance is how close an encode must land before it is accepted.
const rateTolerance = 0.012

// maxFineTuneIterations bounds the feedback loop.
const maxFineTuneIterations = 6

// EncodeTarget encodes until the measured bitrate lands near the target and
// returns the measured statistics. The loop is deterministic because the
// encoder is deterministic for a fixed input.
func (t *Tool) EncodeTarget(refPath, outPath, codec string, s Synthetic, targetKbps float64, preset string) (*FineTune, error) {
	if targetKbps <= 0 {
		return nil, fault.Newf(fault.CodeFFmpegFailed, codec, "target bitrate must be positive")
	}
	ft := &FineTune{}
	encKbps := targetKbps
	for i := 0; i < maxFineTuneIterations; i++ {
		st, err := t.encodeOnce(refPath, outPath, codec, s, encKbps, preset)
		if err != nil {
			return nil, err
		}
		pass := FineTunePass{TargetEncKbps: encKbps, Bytes: st.size, Kbps: st.kbps(s.Seconds)}
		ft.Passes = append(ft.Passes, pass)
		ft.Iterations = i + 1
		if pass.Kbps <= 0 {
			return nil, fault.Newf(fault.CodeFFmpegFailed, codec, "encoder produced an empty stream")
		}
		if math.Abs(pass.Kbps-targetKbps)/targetKbps <= rateTolerance {
			return ft, nil
		}
		encKbps = encKbps * targetKbps / pass.Kbps
		if encKbps < 1 {
			encKbps = 1
		}
	}
	return ft, nil
}

type encodeStat struct {
	size int64
	ms   int64
}

func (e encodeStat) kbps(seconds int) float64 {
	if seconds <= 0 {
		return 0
	}
	return float64(e.size) * 8 / float64(seconds) / 1000
}

func (t *Tool) encodeOnce(refPath, outPath, codec string, s Synthetic, kbps float64, preset string) (encodeStat, error) {
	enc, ok := Encoders[codec]
	if !ok {
		return encodeStat{}, fault.Newf(fault.CodeScanUnsupportedCodec, codec, "no local encoder for codec %q", codec)
	}
	gop := s.FPS / 2
	if gop < 1 {
		gop = 1
	}
	sizeStr := fmt.Sprintf("%dx%d", s.Width, s.Height)
	rateStr := fmt.Sprintf("%.0fk", kbps)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", sizeStr, "-r", strconv.Itoa(s.FPS), "-i", refPath,
		"-c:v", enc.Lib, "-pix_fmt", "yuv420p", "-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop), "-sc_threshold", "0",
	}
	switch codec {
	case "h264":
		args = append(args, "-preset", presetValue(preset, "veryfast"), "-b:v", rateStr,
			"-minrate", rateStr, "-maxrate", rateStr, "-bufsize", fmt.Sprintf("%.0fk", kbps*2),
			"-threads", "1", "-x264-params", "repeat-headers=1:bframes=0", "-f", "h264", outPath)
	case "h265":
		args = append(args, "-preset", presetValue(preset, "ultrafast"), "-b:v", rateStr,
			"-x265-params", fmt.Sprintf("log-level=none:keyint=%d:min-keyint=%d:scenecut=0:repeat-headers=1:pools=1:bframes=0", gop, gop),
			"-f", "hevc", outPath)
	case "av1":
		p := presetValue(preset, "10")
		if n, err := strconv.Atoi(p); err == nil && n > 12 {
			p = "12"
		}
		args = append(args, "-preset", p, "-b:v", rateStr, "-svtav1-params", "lp=1", "-f", "obu", outPath)
	}
	start := time.Now()
	if _, err := t.run(t.FFmpeg, args...); err != nil {
		return encodeStat{}, err
	}
	fi, err := os.Stat(outPath)
	if err != nil {
		return encodeStat{}, fault.Newf(fault.CodeFFmpegFailed, codec, "encoder output missing: %v", err)
	}
	return encodeStat{size: fi.Size(), ms: time.Since(start).Milliseconds()}, nil
}

func presetValue(preset, fallback string) string {
	if preset == "" {
		return fallback
	}
	return preset
}

// EncodeDegraded encodes a deliberately reduced rendition used as a quality
// floor candidate that must be rejected.
func (t *Tool) EncodeDegraded(refPath, outPath, codec string, s Synthetic, scaleWidth int) error {
	enc, ok := Encoders[codec]
	if !ok {
		return fault.Newf(fault.CodeScanUnsupportedCodec, codec, "no local encoder for codec %q", codec)
	}
	gop := s.FPS / 2
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", fmt.Sprintf("%dx%d", s.Width, s.Height),
		"-r", strconv.Itoa(s.FPS), "-i", refPath,
		"-vf", fmt.Sprintf("scale=%d:%d", scaleWidth, s.Height*scaleWidth/s.Width),
		"-c:v", enc.Lib, "-pix_fmt", "yuv420p", "-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop),
	}
	switch codec {
	case "h264":
		args = append(args, "-preset", "ultrafast", "-crf", "51", "-threads", "1", "-f", "h264", outPath)
	case "h265":
		args = append(args, "-preset", "ultrafast", "-crf", "51", "-x265-params", "log-level=none:pools=1", "-f", "hevc", outPath)
	case "av1":
		args = append(args, "-preset", "12", "-crf", "51", "-svtav1-params", "lp=1", "-f", "obu", outPath)
	}
	_, err := t.run(t.FFmpeg, args...)
	return err
}
