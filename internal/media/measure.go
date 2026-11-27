package media

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	"mediacore/internal/archive"
	"mediacore/internal/fault"
)

// MeasurePSNR compares a decoded stream against the raw reference. The
// candidate is rescaled to the reference geometry so a downscaled rendition
// is measured fairly against the same source.
func (t *Tool) MeasurePSNR(refPath, encodedPath string, s Synthetic) (float64, error) {
	args := []string{"-hide_banner", "-nostdin", "-y",
		"-i", encodedPath,
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", fmt.Sprintf("%dx%d", s.Width, s.Height),
		"-r", strconv.Itoa(s.FPS), "-i", refPath,
		"-lavfi", "[0:v]scale=" + strconv.Itoa(s.Width) + ":" + strconv.Itoa(s.Height) +
			":force_original_aspect_ratio=disable,format=yuv420p,settb=AVTB,setpts=N/" + strconv.Itoa(s.FPS) + "/TB[d];" +
			"[1:v]format=yuv420p,settb=AVTB,setpts=N/" + strconv.Itoa(s.FPS) + "/TB[r];[d][r]psnr",
		"-frames:v", strconv.Itoa(s.Frames()),
		"-f", "null", "-",
	}
	stderr, err := t.run(t.FFmpeg, args...)
	if err != nil {
		return 0, err
	}
	m := psnrRe.FindSubmatch(stderr)
	if m == nil {
		stderr, err = t.run(t.FFmpeg, append([]string{"-loglevel", "info"}, args...)...)
		if err != nil {
			return 0, err
		}
		m = psnrRe.FindSubmatch(stderr)
	}
	if m == nil {
		return 0, fault.Newf(fault.CodeFFmpegFailed, "", "could not parse PSNR from FFmpeg output")
	}
	v, err := strconv.ParseFloat(string(m[1]), 64)
	if err != nil {
		return 0, fault.Newf(fault.CodeFFmpegFailed, "", "invalid PSNR %q", m[1])
	}
	return v, nil
}

// ProbeResult is the subset of container timing this project relies on.
type ProbeResult struct {
	VideoSec  float64 `json:"video_sec"`
	AudioSec  float64 `json:"audio_sec"`
	CodecName string  `json:"codec_name"`
}

// ProbeMP4 inspects a locally written MP4 file.
func (t *Tool) ProbeMP4(path string) (*ProbeResult, error) {
	out, err := t.runStdout(t.FFprobe, "-hide_banner", "-v", "error",
		"-show_entries", "stream=codec_type,codec_name,duration",
		"-of", "json", path)
	if err != nil {
		return nil, err
	}
	var probe struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Duration  string `json:"duration"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return nil, fault.Newf(fault.CodeFFmpegFailed, "", "cannot parse ffprobe JSON: %v", err)
	}
	res := &ProbeResult{}
	for _, st := range probe.Streams {
		d, _ := strconv.ParseFloat(st.Duration, 64)
		switch st.CodecType {
		case "video":
			res.VideoSec = d
			res.CodecName = st.CodecName
		case "audio":
			res.AudioSec = d
		}
	}
	return res, nil
}

// FileDigest returns the SHA-256 of a local file for archive fingerprinting.
func FileDigest(path string) (string, error) { return archive.DigestFile(path) }

// Remux packages an elementary stream and its audio into an MP4 archive
// fragment without re-encoding the video.
func (t *Tool) Remux(rawPath, wavPath, outPath, codec string, s Synthetic) error {
	inFmt := map[string]string{"h264": "h264", "h265": "hevc", "av1": "obu"}[codec]
	if inFmt == "" {
		return fault.Newf(fault.CodeScanUnsupportedCodec, codec, "no container mapping for %q", codec)
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-framerate", strconv.Itoa(s.FPS), "-f", inFmt, "-i", rawPath,
		"-i", wavPath,
		"-map", "0:v:0", "-map", "1:a:0",
		"-c:v", "copy", "-c:a", "aac", "-b:a", "96k",
		"-movflags", "+faststart", outPath,
	}
	_, err := t.run(t.FFmpeg, args...)
	return err
}

var psnrRe = regexp.MustCompile(`average:([0-9]+\.?[0-9]*)`)
