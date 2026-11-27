package clock

import (
	"io"

	mp4 "github.com/abema/go-mp4"
)

// ReadMP4Timescale parses an MP4 stream's movie header (moov/mvhd) and
// returns the media timescale in ticks per second. go-mp4 performs the
// container box walking; this helper extracts the single field the kernel
// needs to align an MP4 reference against the integer-nanosecond time base.
func ReadMP4Timescale(r io.ReadSeeker) (uint32, error) {
	var timescale uint32
	found := false
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		if len(h.Path) >= 2 && h.Path[0] == mp4.BoxTypeMoov() &&
			h.Path[len(h.Path)-1] == mp4.BoxTypeMvhd() && !found {
			payload, _, err := h.ReadPayload()
			if err != nil {
				return nil, NewError("clock", CatTruncated,
					"mvhd payload unreadable").Wrap(err)
			}
			if mvhd, ok := payload.(*mp4.Mvhd); ok {
				timescale = mvhd.Timescale
				found = true
			}
		}
		// Depth-first expansion so nested moov children are visited.
		return h.Expand()
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		return 0, NewError("clock", CatTruncated,
			"mp4 structure parse failed").Wrap(err)
	}
	if !found || timescale == 0 {
		return 0, NewError("clock", CatNotFound,
			"no valid moov/mvhd timescale in mp4 input")
	}
	return timescale, nil
}

// MP4TickToNanos converts one MP4 movie timescale tick into media
// nanoseconds with the same overflow guarantees as RTP conversion.
func MP4TickToNanos(ticks int64, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, NewError("clock", CatInvalidParam,
			"illegal mp4 timescale 0")
	}
	return ticksToNanos(ticks, timescale)
}
