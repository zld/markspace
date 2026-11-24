package media

import (
	"mediasync/internal/clock"
)

// Block is one PCM sample block as submitted by the capture side. Blocks
// may start or end mid-frame; the splitter re-assembles whole frames
// across consecutive blocks.
type Block struct {
	Index  int    // arrival ordinal
	Start  int64  // absolute byte offset inside the WAV payload
	Data   []byte // raw bytes belonging to this block
	Frames []byte // bytes covering complete frames after cross-block joins
	Carry  []byte // leading partial frame carried into the next block
}

// ValidateBlock checks a raw block against the declared frame size and
// returns the number of whole frames plus a non-nil truncation error when a
// tail of 1..blockAlign-1 bytes cannot form a frame. Nothing is padded.
func ValidateBlock(data []byte, blockAlign int) (wholeFrames int, err error) {
	if blockAlign <= 0 {
		return 0, clock.NewError(clock.ErrInvalidArgument, "blockAlign must be positive, got %d", blockAlign)
	}
	wholeFrames = len(data) / blockAlign
	if rem := len(data) % blockAlign; rem != 0 {
		return wholeFrames, clock.TruncationError(int64(len(data)-rem), int64(rem), "sample block")
	}
	return wholeFrames, nil
}

// SplitBlocks partitions a PCM payload into blocks of targetBlockSize
// bytes, joining partial frames across block boundaries. Every emitted
// block's Frames length is a multiple of blockAlign; the final block keeps
// any un-joinable tail in Carry and the returned error reports exactly how
// many bytes are missing for a complete final frame.
func SplitBlocks(pcm []byte, blockAlign, targetBlockSize int) ([]Block, error) {
	if blockAlign <= 0 || targetBlockSize < blockAlign {
		return nil, clock.NewError(clock.ErrInvalidArgument,
			"blockAlign=%d targetBlockSize=%d invalid", blockAlign, targetBlockSize)
	}
	if int64(len(pcm)) > MaxInputSize {
		return nil, clock.ErrorAt(clock.ErrLimitExceeded, 0, "PCM payload exceeds 2 GiB")
	}
	var blocks []Block
	var carry []byte
	off := int64(0)
	idx := 0
	for len(pcm) > 0 || len(carry) > 0 {
		take := targetBlockSize - len(carry)
		if take > len(pcm) {
			take = len(pcm)
		}
		joined := make([]byte, 0, len(carry)+take)
		joined = append(joined, carry...)
		joined = append(joined, pcm[:take]...)
		pcm = pcm[take:]

		usable := len(joined) - len(joined)%blockAlign
		b := Block{
			Index: idx,
			Start: off - int64(len(carry)),
			Data:  joined,
		}
		if usable > 0 {
			b.Frames = joined[:usable]
		}
		if len(joined)%blockAlign != 0 {
			b.Carry = joined[usable:]
		}
		blocks = append(blocks, b)
		carry = b.Carry
		off += int64(take)
		idx++

		// No more input bytes: a dangling carry is an unrepairable tail.
		if len(pcm) == 0 && len(carry) > 0 {
			missing := blockAlign - len(carry)
			return blocks, clock.TruncationError(off, int64(missing), "final block frame")
		}
		if len(pcm) == 0 {
			break
		}
	}
	return blocks, nil
}

// FrameAt copies one multi-channel sample frame (blockAlign bytes) from the
// payload at frame index f. It returns an error instead of reading past the
// end of the input.
func FrameAt(pcm []byte, blockAlign int, f int64) ([]byte, error) {
	start := f * int64(blockAlign)
	if f < 0 || start < 0 || start+int64(blockAlign) > int64(len(pcm)) {
		return nil, clock.ErrorAt(clock.ErrOverflow, start,
			"frame %d reaches beyond input length %d", f, len(pcm))
	}
	return pcm[start : start+int64(blockAlign)], nil
}

// SilentFrame returns a zeroed PCM frame, used as an explicit placeholder
// for recoverable packet loss (never for truncated files).
func SilentFrame(blockAlign int) []byte { return make([]byte, blockAlign) }
