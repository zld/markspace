package pipeline

import (
	"archive/internal/fixture"
	"archive/internal/rtp"
)

func transportLayer(lp *layerPlane, frameCount int, mode feedMode, lossBurst int) (*recoveredLayer, error) {
	g := lp.geom
	plan := lossPlan(g, frameCount, mode, lossBurst)
	perFrame := g.bandsPerFrame()
	plan.Payloads = lp.packetPayloads(g.packetSize(frameCount))
	sess, err := fixture.BuildRTP(plan)
	if err != nil {
		return nil, err
	}
	pkts, err := rtp.Decode(sess.Wire)
	if err != nil {
		return nil, err
	}
	rec, err := rtp.Reorder(pkts)
	if err != nil {
		return nil, err
	}
	rl := &recoveredLayer{geom: g, rec: rec, frames: frameCount, packets: len(plan.Payloads)}
	// Sequence spans map to frame boundaries, not packet boundaries: the first
	// packet of frame 0 starts the span, the last packet of the final frame
	// ends it. Using floor division keeps boundaries exactly on the frame grid.
	base := int64(plan.StartSeq)
	if len(rec.Ordered) > 0 {
		first := rec.Ordered[0].SeqExt - base
		last := rec.Ordered[len(rec.Ordered)-1].SeqExt - base
		rl.startNS = first / int64(perFrame) * FrameInterval
		rl.endNS = (last/int64(perFrame) + 1) * FrameInterval
	}
	rl.psnr = reconstructPSNR(lp, rec, base)
	return rl, nil
}

// reconstructPSNR rebuilds each frame from surviving bands, conceals missing
// bands by interpolation, and returns PSNR against the ground truth.
