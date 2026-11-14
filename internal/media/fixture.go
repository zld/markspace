package media

import (
	"context"
	"fmt"
)

// FixtureKinds are the built-in demo inputs: a static clip, a fast-motion
// clip, and a clip whose duration is not a whole multiple of the segment
// length (producing a short final segment).
var FixtureKinds = []string{"static", "motion", "mixed"}

// GenerateFixture synthesizes a deterministic test video with FFmpeg lavfi
// sources. No external media files are required.
func (r Runner) GenerateFixture(ctx context.Context, kind, out string, fps int) error {
	var graph string
	switch kind {
	case "static":
		graph = fmt.Sprintf("color=c=steelblue:size=640x360:rate=%d:duration=5", fps)
	case "motion":
		graph = fmt.Sprintf("testsrc2=size=640x360:rate=%d:duration=5", fps)
	case "mixed":
		// 2s static + 2.5s motion: the 4.5s total is not a whole multiple
		// of the demo's 2s segment, exercising the short-final-segment path.
		graph = fmt.Sprintf("color=c=steelblue:size=640x360:rate=%[1]d:duration=2[a];testsrc2=size=640x360:rate=%[1]d:duration=2.5[b];[a][b]concat=n=2:v=1:a=0", fps)
	default:
		return fmt.Errorf("unknown fixture kind %q", kind)
	}
	args := []string{
		"-hide_banner", "-nostdin", "-y",
		"-f", "lavfi", "-i", graph,
		"-c:v", "libx264", "-preset", "medium", "-crf", "18",
		"-pix_fmt", "yuv420p",
		out,
	}
	_, _, err := r.run(ctx, "ffmpeg", args...)
	if err != nil {
		return fmt.Errorf("generate fixture %s: %w", kind, err)
	}
	return nil
}
