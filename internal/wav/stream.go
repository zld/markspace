package wav

import (
	"io"

	"mediakernel/internal/clock"
)

// Block is one streaming chunk of complete sample frames plus its physical
// location. Memory grows with one block at a time, never the whole PCM.
type Block struct {
	FrameStart int64
	FrameCount int64
	ByteOffset int64
	Data       []byte
}

// BlockIter streams complete-frame blocks from the data payload in order.
type BlockIter struct {
	r          *Reader
	nextFrame  int64
	totalFrame int64
	frameSize  int64
	blockFrame int64
}

// Blocks starts a streaming iterator yielding blockFrame frames per block
// (the final block may be smaller).
func (r *Reader) Blocks(blockFrames int64) (*BlockIter, error) {
	if blockFrames <= 0 {
		return nil, clock.NewError("wav", clock.CatInvalidParam,
			"block frame count must be positive")
	}
	h := r.header
	if h.DataOffset+h.DataLength > h.FileLength {
		return nil, clock.NewError("wav", clock.CatTruncated,
			"data payload ends at %d but file length is %d",
			h.DataOffset+h.DataLength, h.FileLength).At(h.FileLength)
	}
	return &BlockIter{
		r:          r,
		nextFrame:  0,
		totalFrame: h.Frames(),
		frameSize:  h.FrameSize(),
		blockFrame: blockFrames,
	}, nil
}

// Next returns the next block or io.EOF when every frame has been yielded.
// A physically truncated payload fails at the first missing byte.
func (it *BlockIter) Next() (*Block, error) {
	if it.nextFrame >= it.totalFrame {
		return nil, io.EOF
	}
	count := it.blockFrame
	if it.nextFrame+count > it.totalFrame {
		count = it.totalFrame - it.nextFrame
	}
	byteOff := it.r.header.DataOffset + it.nextFrame*it.frameSize
	buf := make([]byte, count*it.frameSize)
	n, err := it.r.file.ReadAt(buf, byteOff)
	if err != nil && err != io.EOF {
		return nil, clock.NewError("wav", clock.CatTruncated,
			"block read failed").At(byteOff).Wrap(err)
	}
	if int64(n) < count*it.frameSize {
		return nil, clock.NewError("wav", clock.CatTruncated,
			"declared %d frames but only %d bytes readable from offset %d",
			count, n, byteOff).At(byteOff + int64(n))
	}
	b := &Block{
		FrameStart: it.nextFrame,
		FrameCount: count,
		ByteOffset: byteOff,
		Data:       buf,
	}
	it.nextFrame += count
	return b, nil
}

// FrameRange is a left-closed right-open interval of sample frames.
type FrameRange struct {
	Start int64
	End   int64
}

// ReadFrameRange returns the raw bytes covering [start, end). The interval
// must be aligned to complete frames; a trailing partial frame at file end
// is reported at the truncation offset rather than silently dropped.
func (r *Reader) ReadFrameRange(fr FrameRange) ([]byte, error) {
	h := r.header
	frameSize := h.FrameSize()
	if fr.Start < 0 || fr.End <= fr.Start {
		return nil, clock.NewError("wav", clock.CatInvalidParam,
			"invalid half-open range [%d,%d)", fr.Start, fr.End)
	}
	if fr.End > h.Frames() {
		endOff := h.DataOffset + fr.End*frameSize
		if endOff > h.FileLength {
			// Partial/trailing frame: locate the first missing byte.
			return nil, clock.NewError("wav", clock.CatTruncated,
				"range end frame %d beyond readable data", fr.End).At(h.FileLength)
		}
		return nil, clock.NewError("wav", clock.CatBadLength,
			"range end frame %d beyond declared %d frames", fr.End, h.Frames()).
			At(h.DataOffset + h.Frames()*frameSize)
	}
	startOff := h.DataOffset + fr.Start*frameSize
	length := (fr.End - fr.Start) * frameSize
	buf := make([]byte, length)
	n, err := r.file.ReadAt(buf, startOff)
	if err != nil && err != io.EOF {
		return nil, clock.NewError("wav", clock.CatTruncated,
			"range read failed").At(startOff).Wrap(err)
	}
	if int64(n) < length {
		return nil, clock.NewError("wav", clock.CatTruncated,
			"range truncated: got %d of %d bytes", n, length).At(startOff + int64(n))
	}
	return buf, nil
}
