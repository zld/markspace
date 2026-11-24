package repair

import (
	"fmt"

	"media-archiver/internal/media"
)

// CheckFormatAndBoundaries independently verifies channel/frame geometry.
func CheckFormatAndBoundaries(format media.PCMFormat, blocks []media.RawBlock) error {
	if err := format.Validate(); err != nil {
		return err
	}
	frameBytes := format.FrameByteCount()
	if frameBytes != int(format.Channels)*int(format.BitsPerSample)/8 {
		return media.NewError(media.CodeInvalidConfig, media.StageTransform, -1, "frame byte count mismatch", nil)
	}
	for _, b := range blocks {
		if int64(len(b.Data)) < b.CompleteFrames*int64(frameBytes) {
			return media.NewError(media.CodeTruncated, media.StageTransform, b.DataOffset,
				fmt.Sprintf("block %d payload shorter than complete frame count", b.Index), nil)
		}
		if b.TailBytes >= frameBytes {
			return media.NewError(media.CodeInternal, media.StageTransform, b.DataOffset,
				"tail byte count exceeds one frame remainder", nil)
		}
	}
	return nil
}

// AlignedPCM returns only complete frame bytes. It never zero-fills a tail.
func AlignedPCM(format media.PCMFormat, b media.RawBlock) ([]byte, *media.Error) {
	frameBytes := format.FrameByteCount()
	usable := b.CompleteFrames * int64(frameBytes)
	if usable > int64(len(b.Data)) {
		return nil, media.NewError(media.CodeTruncated, media.StageTransform, b.CorruptOffset,
			"refusing to return partial frame payload", nil)
	}
	pcm := append([]byte(nil), b.Data[:usable]...)
	return pcm, nil
}

// Assemble runs boundary checks, timestamp repair, and PCM digest assignment.
func Assemble(format media.PCMFormat, raw []media.RawBlock, tb media.Timebase, policy Policy) (*TimelineResult, error) {
	if err := CheckFormatAndBoundaries(format, raw); err != nil {
		return nil, err
	}
	timeline, err := RepairTimeline(raw, tb, policy)
	if err != nil {
		return nil, err
	}
	for i := range timeline.Blocks {
		pcm, issue := AlignedPCM(format, raw[i])
		if issue != nil {
			return nil, issue
		}
		timeline.Blocks[i].PCM = pcm
		timeline.Blocks[i].AudioSHA256 = media.SHA256Hex(pcm)
	}
	return timeline, nil
}

// ConcatPCM concatenates canonical block PCM in timeline order.
func ConcatPCM(blocks []media.CanonicalBlock) []byte {
	total := 0
	for _, b := range blocks {
		total += len(b.PCM)
	}
	out := make([]byte, 0, total)
	for _, b := range blocks {
		out = append(out, b.PCM...)
	}
	return out
}

// CountBadBlocks counts blocks carrying unrepaired content corruption.
func CountBadBlocks(blocks []media.CanonicalBlock) int {
	n := 0
	for _, b := range blocks {
		if b.Bad {
			n++
		}
	}
	return n
}
