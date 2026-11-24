package pipeline

import (
	"math"

	"archive/internal/clock"
	"archive/internal/fixture"
	"archive/internal/normalize"
	"archive/internal/render"
	"archive/internal/rtp"
)

// layerGeom is one representation's raster and its packet banding.
type layerGeom struct {
	name     string
	kbps     int
	width    int
	height   int
	bandRows int
	// qStep is the deterministic quantisation step applied to transmitted luma.
	// Higher-rate layers quantise less, which yields the expected PSNR ordering.
	qStep int
}

func defaultGeometries() []layerGeom {
	return []layerGeom{
		{name: "rep-1100k", kbps: 1100, width: 84, height: 63, bandRows: 7, qStep: 4},
		{name: "rep-900k", kbps: 900, width: 72, height: 54, bandRows: 6, qStep: 8},
		{name: "rep-420k", kbps: 420, width: 48, height: 36, bandRows: 4, qStep: 12},
	}
}

func (g layerGeom) bandsPerFrame() int {
	n := (g.height + g.bandRows - 1) / g.bandRows
	if n <= 0 {
		n = 1
	}
	return n
}

// packetSize is chosen so the layer's total payload meets its target bitrate
// for the session duration.
func (g layerGeom) packetSize(frameCount int) int {
	packets := frameCount * g.bandsPerFrame()
	if packets <= 0 {
		return 1
	}
	durationNS := float64(frameCount) * float64(FrameInterval)
	totalBytes := float64(g.kbps) * 1000 / 8 * (durationNS / 1e9)
	sz := int(totalBytes) / packets
	if sz < 1 {
		sz = 1
	}
	return sz
}

func bandContentLen(g layerGeom, band int) int {
	rows := g.bandRows
	if (band+1)*g.bandRows > g.height {
		rows = g.height - band*g.bandRows
	}
	if rows < 0 {
		rows = 0
	}
	return rows * g.width
}

// layerPlane is each frame's ground-truth luma at the layer raster.
type layerPlane struct {
	geom   layerGeom
	frames [][]uint8
}

func buildLayerPlane(g layerGeom, res *normalize.Resolution, seed uint64, frameCount int) (*layerPlane, error) {
	lp := &layerPlane{geom: g}
	for f := 0; f < frameCount; f++ {
		ref, err := render.Reference(res.StoredWidth, res.StoredHeight, seed, f)
		if err != nil {
			return nil, err
		}
		out, err := render.ApplyTransform(ref, res)
		if err != nil {
			return nil, err
		}
		plane, err := render.Downsample(render.Luma(out), out.Bounds().Dx(), out.Bounds().Dy(), g.width, g.height)
		if err != nil {
			return nil, err
		}
		lp.frames = append(lp.frames, plane)
	}
	return lp, nil
}

// packetPayloads encodes each frame's luma bands into fixed-size payloads so
// recovered packets carry real image data.
func (lp *layerPlane) packetPayloads(packetSize int) [][]byte {
	g := lp.geom
	per := g.bandsPerFrame()
	out := make([][]byte, 0, len(lp.frames)*per)
	for _, frame := range lp.frames {
		for b := 0; b < per; b++ {
			n := bandContentLen(g, b)
			content := make([]byte, n)
			for r := 0; r < g.bandRows && b*g.bandRows+r < g.height; r++ {
				y := b*g.bandRows + r
				copy(content[r*g.width:(r+1)*g.width], frame[y*g.width:(y+1)*g.width])
			}
			for i := range content {
				content[i] = quantize(content[i], g.qStep)
			}
			payload := content
			if len(payload) < packetSize {
				payload = append(payload, make([]byte, packetSize-len(payload))...)
			}
			out = append(out, payload)
		}
	}
	return out
}

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
func reconstructPSNR(lp *layerPlane, rec *rtp.Result, base int64) float64 {
	g := lp.geom
	per := g.bandsPerFrame()
	type key struct{ frame, band int }
	present := make(map[key][]byte)
	for _, p := range rec.Ordered {
		idx := p.SeqExt - base
		if idx < 0 {
			continue
		}
		frame := int(idx) / per
		band := int(idx) % per
		if frame >= len(lp.frames) || band >= per {
			continue
		}
		n := bandContentLen(g, band)
		if len(p.Payload) < n {
			n = len(p.Payload)
		}
		present[key{frame, band}] = p.Payload[:n]
	}
	var mse float64
	var count int
	for f, truth := range lp.frames {
		got := make([]uint8, len(truth))
		filled := make([]bool, per)
		for b := 0; b < per; b++ {
			data, ok := present[key{f, b}]
			if !ok {
				continue
			}
			filled[b] = true
			copyBand(got, g, b, data)
		}
		concealBands(got, g, filled)
		for i := range truth {
			d := float64(got[i]) - float64(truth[i])
			mse += d * d
			count++
		}
	}
	if count == 0 {
		return 0
	}
	mse /= float64(count)
	if mse == 0 {
		return render.LosslessPSNRCap
	}
	return 10 * math.Log10(255*255/mse)
}

func copyBand(dst []uint8, g layerGeom, band int, data []byte) {
	n := bandContentLen(g, band)
	if len(data) < n {
		n = len(data)
	}
	start := band * g.bandRows * g.width
	copy(dst[start:start+n], data[:n])
}

// concealBands fills missing bands by linear interpolation between the nearest
// known rows above and below. With no surviving band the region is left black.
func concealBands(got []uint8, g layerGeom, filled []bool) {
	per := g.bandsPerFrame()
	for b := 0; b < per; b++ {
		if filled[b] {
			continue
		}
		above, below := -1, -1
		for i := b - 1; i >= 0; i-- {
			if filled[i] {
				above = i
				break
			}
		}
		for i := b + 1; i < per; i++ {
			if filled[i] {
				below = i
				break
			}
		}
		for r := 0; r < g.bandRows; r++ {
			y := b*g.bandRows + r
			if y >= g.height {
				break
			}
			rowDst := got[y*g.width : (y+1)*g.width]
			switch {
			case above >= 0 && below >= 0:
				ay := above*g.bandRows + g.bandRows - 1
				by := below * g.bandRows
				span := by - ay
				frac := float64(y-ay) / float64(span)
				for x := 0; x < g.width; x++ {
					a := float64(got[ay*g.width+x])
					bb := float64(got[by*g.width+x])
					rowDst[x] = clampByte(a + frac*(bb-a))
				}
			case above >= 0:
				ay := above*g.bandRows + g.bandRows - 1
				copy(rowDst, got[ay*g.width:(ay+1)*g.width])
			case below >= 0:
				by := below * g.bandRows
				copy(rowDst, got[by*g.width:(by+1)*g.width])
			default:
				for x := range rowDst {
					rowDst[x] = 0
				}
			}
		}
	}
}

// quantize rounds v to the nearest multiple of step, clamping to 0..255.
func quantize(v uint8, step int) uint8 {
	if step <= 1 {
		return v
	}
	q := (int(v) + step/2) / step * step
	if q > 255 {
		q = 255
	}
	if q < 0 {
		q = 0
	}
	return uint8(q)
}

func clampByte(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// layerPSNRFloor documents the acceptance floor; the pipeline reports a
// failure instead of clamping a genuinely lossy result.
const layerPSNRFloor = 30.0

// checkPSNR converts a below-floor measurement into a classified error so the
// caller can isolate the layer rather than emit a misleading quality figure.
func checkPSNR(name string, psnr float64) error {
	if psnr < layerPSNRFloor {
		return clock.Newf(clock.KindMisaligned, "layer %s psnr %.2f dB below floor %.1f dB", name, psnr, layerPSNRFloor)
	}
	return nil
}
