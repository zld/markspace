package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"replay/internal/cmaf"
)

// masterOpts describes one ffmpeg render of the source media.
type masterOpts struct {
	durationSec int
	audioRate   int // output sample rate
	audioCh     int // output channels
	colorTags   []string
	writeColr   bool
}

// defaultMaster is the normal fixture: 640x360@30 bt709, stereo 48kHz.
func defaultMaster() masterOpts {
	return masterOpts{
		durationSec: 20,
		audioRate:   48000,
		audioCh:     2,
		colorTags:   []string{"bt709", "bt709", "bt709"},
		writeColr:   true,
	}
}

// renderMaster renders a fragmented MP4 (init + moof/mdat pairs in one
// file) with ffmpeg. Deterministic encode settings keep re-runs stable.
func renderMaster(path string, o masterOpts) error {
	movflags := "frag_keyframe+empty_moov+default_base_moof"
	if o.writeColr {
		movflags += "+write_colr"
	}
	args := []string{
		"-y", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=640x360:rate=30:duration=%d", o.durationSec),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:sample_rate=%d:duration=%d", o.audioRate, o.durationSec),
		"-c:v", "libx264", "-g", "60", "-keyint_min", "60", "-sc_threshold", "0",
		"-pix_fmt", "yuv420p", "-vf", videoFilter(o),
		"-c:a", "aac", "-b:a", "128k",
		"-ac", fmt.Sprint(o.audioCh), "-ar", fmt.Sprint(o.audioRate),
		"-movflags", movflags,
		"-frag_duration", "2000000",
	}
	args = append(args, path)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg render: %w: %s", err, out)
	}
	return nil
}

// videoFilter builds the source filter chain. Color tags are applied with
// setparams: output-side -color_* options do not reach the colr box. A
// light noise floor keeps the encode realistic; CBR filler maintains the
// bitrate regardless.
func videoFilter(o masterOpts) string {
	vf := "noise=alls=2:allf=t"
	if len(o.colorTags) == 3 {
		vf += fmt.Sprintf(",setparams=color_primaries=%s:color_trc=%s:colorspace=%s",
			o.colorTags[0], o.colorTags[1], o.colorTags[2])
	}
	return vf
}

// splitMaster splits a fragmented MP4 into init.mp4 plus one .m4s file per
// moof..(next moof) range, using the library's box walk for offsets.
func splitMaster(masterPath, setDir string) (int, error) {
	p := cmaf.NewParser(nil)
	boxes, err := p.TopLevelBoxes(masterPath)
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(masterPath)
	if err != nil {
		return 0, err
	}
	firstMoof := -1
	for i, b := range boxes {
		if b.Type.String() == "moof" {
			firstMoof = i
			break
		}
	}
	if firstMoof < 0 {
		return 0, fmt.Errorf("split: %s: no moof boxes found", masterPath)
	}
	if err := ensureDir(setDir); err != nil {
		return 0, err
	}
	initEnd := int64(boxes[firstMoof].Offset)
	if err := os.WriteFile(join(setDir, "init.mp4"), data[:initEnd], 0o644); err != nil {
		return 0, err
	}
	n := 0
	for i := firstMoof; i < len(boxes); i++ {
		if boxes[i].Type.String() != "moof" {
			continue
		}
		start := int64(boxes[i].Offset)
		end := int64(len(data))
		if i+1 < len(boxes) {
			// segment runs to the next moof, skipping nothing in between
			for j := i + 1; j < len(boxes); j++ {
				if boxes[j].Type.String() == "moof" {
					end = int64(boxes[j].Offset)
					break
				}
			}
		}
		n++
		name := fmt.Sprintf("seg_%02d.m4s", n)
		if err := os.WriteFile(join(setDir, name), data[start:end], 0o644); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// concatFiles writes dst = concatenation of srcs.
func concatFiles(dst string, srcs ...string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	for _, s := range srcs {
		b, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		if _, err := out.Write(b); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies one file.
func copyFile(dst, src string) error {
	return concatFiles(dst, src)
}

// ensureDir creates dir if missing.
func ensureDir(dir string) error { return os.MkdirAll(dir, 0o755) }

// join is a small alias to keep call sites short.
func join(elem ...string) string { return filepath.Join(elem...) }
