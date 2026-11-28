package quality

import (
	"archive-lab/internal/media"
)

// Frame encoding layout (all integers, no floating point on the wire):
//
//	bytes 0-1  quantiser in hundredths (100..25500, big endian)
//	byte 2     Rice parameter k (0..15)
//	byte 3..   bit-packed blocks, MSB first
//
// Each 8x8 block starts with one flag bit: 0 means every quantised residual
// in the block is zero, 1 means the residual stream follows in raster order.
// Prediction uses reconstructed neighbours, so encoder and decoder stay in
// lockstep and quantisation error never compounds. The stream is decodable,
// so the measured PSNR is a property of the encoded payload, not of the
// measurement code alone.

// EncodeFrame encodes one luma plane with a quantiser in hundredths of a
// step (qMilli=100 means a step of 1) and returns the payload plus the exact
// squared error introduced by quantisation, measured against the input.
func EncodeFrame(luma []byte, width, height, qMilli int) ([]byte, Stats, error) {
	if width <= 0 || height <= 0 || len(luma) != width*height {
		return nil, Stats{}, media.New(media.StagePlan, "encode", "plane size %d does not match %dx%d", len(luma), width, height)
	}
	if qMilli < 100 || qMilli > 25500 {
		return nil, Stats{}, media.New(media.StagePlan, "encode", "quantiser %d outside 1.00..255.00", qMilli)
	}
	qr, sse := quantise(luma, width, height, qMilli)
	k, bits := chooseRice(qr, width, height)
	bw := newBitWriter(3 + (bits+7)/8)
	bw.writeBits(qMilli, 16)
	bw.writeBits(k, 8)
	emitBlocks(qr, width, height, k, bw)
	bw.flush()
	payload := bw.bytes()
	if want := 3 + (bits+7)/8; len(payload) != want {
		return nil, Stats{}, media.New(media.StagePlan, "encode", "bit accounting mismatch: %d != %d", len(payload), want)
	}
	return payload, Stats{Bytes: len(payload), SSE: sse, Pixels: int64(len(luma))}, nil
}

// QuantiseOnly reports the estimated payload size for one frame at a
// quantiser without materialising it.
func QuantiseOnly(luma []byte, width, height, qMilli int) (int, int64, error) {
	if width <= 0 || height <= 0 || len(luma) != width*height {
		return 0, 0, media.New(media.StagePlan, "encode", "plane size %d does not match %dx%d", len(luma), width, height)
	}
	if qMilli < 100 || qMilli > 25500 {
		return 0, 0, media.New(media.StagePlan, "encode", "quantiser %d outside 1.00..255.00", qMilli)
	}
	qr, sse := quantise(luma, width, height, qMilli)
	_, bits := chooseRice(qr, width, height)
	return 3 + (bits+7)/8, sse, nil
}

// DecodeFrame reverses EncodeFrame.
func DecodeFrame(payload []byte, width, height int) ([]byte, error) {
	if len(payload) < 4 || width <= 0 || height <= 0 {
		return nil, media.New(media.StageExecute, "decode", "payload too short")
	}
	qMilli := int(payload[0])<<8 | int(payload[1])
	k := int(payload[2])
	if qMilli < 100 || k > 15 {
		return nil, media.New(media.StageExecute, "decode", "invalid header q=%d k=%d", qMilli, k)
	}
	br := newBitReader(payload[3:])
	recon := make([]byte, width*height)
	idx := 0
	for by := 0; by < height; by += 8 {
		for bx := 0; bx < width; bx += 8 {
			nonzero := br.bit()
			for y := by; y < by+8 && y < height; y++ {
				for x := bx; x < bx+8 && x < width; x++ {
					pred := predict(recon, width, x, y)
					if nonzero {
						qr := unzigzag(br.rice(k))
						recon[idx] = byte(clamp8(pred + qr*qMilli/100))
					} else {
						recon[idx] = byte(clamp8(pred))
					}
					idx++
				}
			}
		}
	}
	return recon, nil
}

// chooseRice picks the Rice parameter with the exact smallest encoded cost.
// Zero blocks cost one flag bit; a non-zero block costs one flag bit plus the
// residual code for every pixel in it, including zero residuals.
func chooseRice(qr []int16, width, height int) (int, int) {
	var hist [512]int // zigzag residual range 0..510
	blocks, nonzeroBlocks := 0, 0
	idx := 0
	for by := 0; by < height; by += 8 {
		for bx := 0; bx < width; bx += 8 {
			blocks++
			nonzero := false
			for y := by; y < by+8 && y < height; y++ {
				for x := bx; x < bx+8 && x < width; x++ {
					z := zigzag(int(qr[idx]))
					hist[z]++
					if z != 0 {
						nonzero = true
					}
					idx++
				}
			}
			if nonzero {
				nonzeroBlocks++
			}
		}
	}
	bestK, bestBits := 0, int(^uint(0)>>1)
	for k := 0; k <= 15; k++ {
		bits := blocks
		for z, n := range hist {
			if n == 0 || z == 0 {
				continue
			}
			bits += n * riceBits(z, k)
		}
		// Zero residuals inside non-zero blocks still cost riceBits(0,k),
		// except those in blocks that emitted no residual stream at all.
		zeroResiduals := hist[0] - zeroBlockPixels(qr, width, height)
		bits += zeroResiduals * riceBits(0, k)
		if bits < bestBits {
			bestK, bestBits = k, bits
		}
	}
	return bestK, bestBits
}

// zeroBlockPixels counts pixels that belong to all-zero blocks.
func zeroBlockPixels(qr []int16, width, height int) int {
	total := 0
	idx := 0
	for by := 0; by < height; by += 8 {
		for bx := 0; bx < width; bx += 8 {
			nonzero := false
			count := 0
			for y := by; y < by+8 && y < height; y++ {
				for x := bx; x < bx+8 && x < width; x++ {
					if qr[idx] != 0 {
						nonzero = true
					}
					count++
					idx++
				}
			}
			if !nonzero {
				total += count
			}
		}
	}
	return total
}

// quantise runs the full block loop for one quantiser and Rice parameter,
// returning the residual grid, the squared error and the required bit count.
func quantise(luma []byte, width, height, qMilli int) ([]int16, int64) {
	qrAll := make([]int16, len(luma))
	recon := make([]byte, len(luma))
	var sse int64
	idx := 0
	for by := 0; by < height; by += 8 {
		for bx := 0; bx < width; bx += 8 {
			for y := by; y < by+8 && y < height; y++ {
				for x := bx; x < bx+8 && x < width; x++ {
					p := int(luma[idx])
					pred := predict(recon, width, x, y)
					qr := clampResidual(roundDiv((p-pred)*100, qMilli))
					qrAll[idx] = int16(qr)
					v := clamp8(pred + qr*qMilli/100)
					recon[idx] = byte(v)
					d := int64(p) - int64(v)
					sse += d * d
					idx++
				}
			}
		}
	}
	return qrAll, sse
}

func emitBlocks(qrAll []int16, width, height, k int, bw *bitWriter) {
	idx := 0
	for by := 0; by < height; by += 8 {
		for bx := 0; bx < width; bx += 8 {
			start := idx
			nonzero := false
			for y := by; y < by+8 && y < height; y++ {
				for x := bx; x < bx+8 && x < width; x++ {
					if qrAll[idx] != 0 {
						nonzero = true
					}
					idx++
				}
			}
			if !nonzero {
				bw.writeBit(0)
				continue
			}
			bw.writeBit(1)
			for _, v := range qrAll[start:idx] {
				bw.writeRice(zigzag(int(v)), k)
			}
		}
	}
}

func predict(recon []byte, width, x, y int) int {
	if x == 0 && y == 0 {
		return 128
	}
	if y == 0 {
		return int(recon[y*width+x-1])
	}
	if x == 0 {
		return int(recon[(y-1)*width+x])
	}
	return (int(recon[y*width+x-1]) + int(recon[(y-1)*width+x]) + 1) / 2
}

func clampResidual(v int) int {
	if v > 255 {
		return 255
	}
	if v < -255 {
		return -255
	}
	return v
}

func roundDiv(v, q int) int {
	if v >= 0 {
		return (v + q/2) / q
	}
	return -((-v + q/2) / q)
}

func clamp8(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

func zigzag(v int) int {
	if v >= 0 {
		return 2 * v
	}
	return -2*v - 1
}

func unzigzag(z int) int {
	if z%2 == 0 {
		return z / 2
	}
	return -(z + 1) / 2
}

func riceBits(z, k int) int { return (z >> uint(k)) + 1 + k }

type bitWriter struct {
	buf  []byte
	cur  byte
	nbit uint
}

func newBitWriter(capacity int) *bitWriter { return &bitWriter{buf: make([]byte, 0, capacity)} }

func (w *bitWriter) writeBit(b int) { w.writeBits(b&1, 1) }

func (w *bitWriter) writeBits(v, n int) {
	for i := n - 1; i >= 0; i-- {
		w.cur = (w.cur << 1) | byte((v>>uint(i))&1)
		w.nbit++
		if w.nbit == 8 {
			w.buf = append(w.buf, w.cur)
			w.cur, w.nbit = 0, 0
		}
	}
}

func (w *bitWriter) writeRice(z, k int) {
	for i := 0; i < z>>uint(k); i++ {
		w.writeBit(1)
	}
	w.writeBit(0)
	if k > 0 {
		w.writeBits(z&((1<<uint(k))-1), k)
	}
}

func (w *bitWriter) flush() {
	if w.nbit > 0 {
		w.cur <<= 8 - w.nbit
		w.buf = append(w.buf, w.cur)
		w.cur, w.nbit = 0, 0
	}
}

func (w *bitWriter) bytes() []byte { return w.buf }
func (w *bitWriter) len() int      { return len(w.buf) }

type bitReader struct {
	data []byte
	pos  int
	cur  byte
	left uint
}

func newBitReader(data []byte) *bitReader { return &bitReader{data: data} }

func (r *bitReader) bit() bool {
	if r.left == 0 {
		if r.pos >= len(r.data) {
			return false
		}
		r.cur = r.data[r.pos]
		r.pos++
		r.left = 8
	}
	b := r.cur&0x80 != 0
	r.cur <<= 1
	r.left--
	return b
}

func (r *bitReader) bits(n int) int {
	v := 0
	for i := 0; i < n; i++ {
		v <<= 1
		if r.bit() {
			v |= 1
		}
	}
	return v
}

func (r *bitReader) rice(k int) int {
	q := 0
	for r.bit() {
		q++
		if q > 1<<20 {
			return 0
		}
	}
	return q<<uint(k) | r.bits(k)
}
