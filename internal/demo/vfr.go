package demo

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediad/internal/clock"
)

// BuildVFR creates explicit alternating short/long frame intervals summing
// exactly to the program duration and validates them on the unified clock.
func (p *Program) addVFR(total clock.Span) {
	pts := []int64{}
	t := total.Start
	for i := 0; t < total.End; i++ {
		pts = append(pts, t)
		if i%2 == 0 {
			t += 16_666_667
		} else {
			t += 33_333_333
		}
	}
	frames := clock.NormalizeVFR(pts, clock.NanoScale, "vfr:0", 33_333_333, 100_000_000, &p.Errors)
	p.VFRFrames = frames
	path := filepath.Join(p.Opt.Out, "fixtures", "vfr-timeline.json")
	if data, err := json.MarshalIndent(map[string]any{
		"track":  "vfr:0",
		"frames": frames,
	}, "", "  "); err == nil {
		_ = os.WriteFile(path, append(data, '\n'), 0o644)
	}
}
