package archive

import (
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

// TrackTiming is one track's declared timing read from an archived MP4
// fragment.
type TrackTiming struct {
	Handler    string     `json:"handler"`
	Timescale  uint32     `json:"timescale"`
	Duration   clock.Time `json:"duration_ns"`
	EditOffset clock.Time `json:"edit_offset_ns"`
}

// FragmentTiming is the timing summary of an archive fragment. It is read
// with abema/go-mp4 so the check does not depend on the encoder's own probe.
type FragmentTiming struct {
	MovieTimescale uint32        `json:"movie_timescale"`
	MovieDuration  clock.Time    `json:"movie_duration_ns"`
	Tracks         []TrackTiming `json:"tracks"`
}

// ValidateFragment opens an MP4 archive fragment, checks that the container
// is structurally readable by go-mp4, and returns each track's duration on
// the shared nanosecond clock. Every track must be within tolerance of the
// expected duration; a mismatch is a stable MC_ARCHIVE_UNVERIFIED fault.
func ValidateFragment(path string, expected clock.Time, tolerance clock.Time) (*FragmentTiming, error) {
	if expected <= 0 {
		return nil, fault.Newf(fault.CodeArchiveUnverified, "", "expected duration must be positive")
	}
	if tolerance <= 0 {
		tolerance = clock.Millisecond
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.Newf(fault.CodeArchiveUnverified, "", "cannot open archive fragment: %v", err)
	}
	defer f.Close()
	timing, err := readFragmentTiming(f)
	if err != nil {
		return nil, err
	}
	if len(timing.Tracks) == 0 {
		return nil, fault.Newf(fault.CodeArchiveUnverified, "", "archive fragment declares no tracks")
	}
	for _, tr := range timing.Tracks {
		d := tr.Duration - tr.EditOffset
		if d < 0 {
			d = -d
		}
		// AAC pads its final frame, so a sound track may legitimately run
		// slightly long. Accept a bounded excess while still rejecting a
		// genuinely wrong duration.
		limit := tolerance
		if tr.Handler == "soun" {
			limit = tolerance + 30*clock.Millisecond
		}
		if diff := d - expected; diff > limit || diff < -tolerance {
			return timing, fault.Newf(fault.CodeArchiveUnverified, tr.Handler,
				"%s track duration %s differs from %s by more than %s", tr.Handler, d, expected, limit)
		}
	}
	return timing, nil
}

func readFragmentTiming(r io.ReadSeeker) (*FragmentTiming, error) {
	timing := &FragmentTiming{}
	var cur TrackTiming
	have := false
	flush := func() {
		if !have {
			return
		}
		timing.Tracks = append(timing.Tracks, cur)
		cur = TrackTiming{}
		have = false
	}
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeEdts():
			return h.Expand()
		case mp4.BoxTypeMvhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			mvhd := box.(*mp4.Mvhd)
			timing.MovieTimescale = mvhd.Timescale
			d := uint64(mvhd.DurationV0)
			if mvhd.GetVersion() == 1 {
				d = mvhd.DurationV1
			}
			if ns, err := clock.FromTimescale(int64(d), mvhd.Timescale); err == nil {
				timing.MovieDuration = ns
			}
		case mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			// A new media header starts a new track; flush the previous one.
			flush()
			mdhd := box.(*mp4.Mdhd)
			cur.Timescale = mdhd.Timescale
			have = true
			d := uint64(mdhd.DurationV0)
			if mdhd.GetVersion() == 1 {
				d = mdhd.DurationV1
			}
			if ns, err := clock.FromTimescale(int64(d), mdhd.Timescale); err == nil {
				cur.Duration = ns
			}
		case mp4.BoxTypeHdlr():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			hdlr := box.(*mp4.Hdlr)
			if have {
				cur.Handler = string(hdlr.HandlerType[:])
			}
		case mp4.BoxTypeElst():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			elst := box.(*mp4.Elst)
			if have && len(elst.Entries) > 0 {
				entry := elst.Entries[0]
				var media int64
				if elst.GetVersion() == 1 {
					media = entry.MediaTimeV1
				} else {
					media = int64(entry.MediaTimeV0)
				}
				if media < 0 {
					media = 0
				}
				if ns, err := clock.FromTimescale(media, cur.Timescale); err == nil {
					cur.EditOffset = ns
				}
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, fault.Newf(fault.CodeArchiveUnverified, "", "archive fragment is not readable MP4: %v", err)
	}
	flush()
	if timing.MovieTimescale == 0 {
		return nil, fault.Newf(fault.CodeArchiveUnverified, "", "archive fragment has no movie header")
	}
	return timing, nil
}

// MP4Digest returns the SHA-256 of an archive fragment file.
func MP4Digest(path string) (Digest, error) { return DigestFile(path) }
