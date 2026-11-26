// Package fixture generates small deterministic local media and traces.
package fixture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Paths are the generated acceptance inputs.
type Paths struct {
	Dir          string `json:"dir"`
	StandardWebM string `json:"standard_webm"`
	DamagedWebM  string `json:"damaged_webm"`
	Events       string `json:"events"`
	StandardRTP  string `json:"standard_rtp"`
	BurstRTP     string `json:"burst_rtp"`
	ReferenceMP4 string `json:"reference_mp4"`
	MP4Timeline  string `json:"mp4_timeline"`
}

// Options controls fixture generation.
type Options struct {
	Dir  string
	Seed int64
}

// Generate creates every sample under Opts.Dir. All media is synthesized by
// local FFmpeg; no external sample material is used.
func Generate(opts Options) (*Paths, error) {
	if opts.Dir == "" {
		return nil, fmt.Errorf("fixture directory is required")
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, err
	}
	p := &Paths{
		Dir:          opts.Dir,
		StandardWebM: filepath.Join(opts.Dir, "standard.webm"),
		DamagedWebM:  filepath.Join(opts.Dir, "damaged.webm"),
		Events:       filepath.Join(opts.Dir, "events.jsonl"),
		StandardRTP:  filepath.Join(opts.Dir, "trace.rtp"),
		BurstRTP:     filepath.Join(opts.Dir, "burst-loss.rtp"),
		ReferenceMP4: filepath.Join(opts.Dir, "timeline-reference.mp4"),
		MP4Timeline:  filepath.Join(opts.Dir, "mp4-timeline.json"),
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found in PATH; install with: brew install ffmpeg")
	}
	if err := generateWebM(ffmpeg, p.StandardWebM); err != nil {
		return nil, err
	}
	if err := generateMP4(ffmpeg, p.ReferenceMP4); err != nil {
		return nil, err
	}
	if err := writeEvents(p.Events); err != nil {
		return nil, err
	}
	if err := writeRTPPair(p, opts.Seed); err != nil {
		return nil, err
	}
	if err := makeDamagedWebM(p.StandardWebM, p.DamagedWebM); err != nil {
		return nil, err
	}
	if err := writeMP4Timeline(p.ReferenceMP4, p.MP4Timeline); err != nil {
		return nil, err
	}
	return p, nil
}

func runFFmpeg(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg %s: %w\n%s", firstArg(args), err, tail(out, 4000))
	}
	return nil
}

func firstArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return "?"
}

func tail(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return "..." + string(b[len(b)-n:])
}
