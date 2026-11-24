package pipeline

import (
	"archive/internal/fixture"
	"archive/internal/rtp"
)

type feedMode int

const (
	modeClean feedMode = iota
	modeBurst
)

// lossPlan assigns injected faults. Every layer gets duplicates and one-slot
// reorders. In burst mode one layer loses a mid-stream run (gap markers, span
// intact, concealed pixels) and another loses its entire first frame, which
// moves its confirmed start a full frame off the shared origin so fan-in can
// isolate it.
func lossPlan(g layerGeom, frameCount int, mode feedMode, lossBurst int) fixture.RTPPlan {
	packets := frameCount * g.bandsPerFrame()
	plan := fixture.RTPPlan{
		PacketCount: packets,
		StartSeq:    1,
		StartTS:     0,
		TSStep:      uint32(rtp.TSOffset),
	}
	for i := 7; i < packets; i += 7 {
		plan.DuplicateAt = append(plan.DuplicateAt, i)
	}
	for i := 3; i+1 < packets; i += 9 {
		plan.ReorderAt = append(plan.ReorderAt, i)
	}
	if mode == modeBurst && lossBurst > 0 {
		switch g.name {
		case "rep-900k":
			plan.LossStart = packets / 2
			plan.LossBurst = lossBurst
		case "rep-420k":
			// Drop the whole first frame: the layer starts one frame late.
			plan.LossStart = 0
			plan.LossBurst = g.bandsPerFrame()
		}
	}
	return plan
}

// recoveredLayer is the transported outcome of one representation.
type recoveredLayer struct {
	geom    layerGeom
	rec     *rtp.Result
	startNS int64
	endNS   int64
	psnr    float64
	frames  int
	packets int
}
