package container

import (
	"fmt"
	"os"

	"github.com/abema/go-mp4"

	"mediacore/internal/core"
)

func (s *Scan) readTracks(moov *Box) *core.Error {
	var first *core.Error
	var traks []*Box
	for _, c := range moov.Children {
		if c.Type == "trak" {
			traks = append(traks, c)
		}
	}
	if len(traks) == 0 {
		return core.New(core.CodeMissingTrack, core.StageScan, false, "moov contains no trak")
	}
	if s.limTracksExceeded(len(traks)) {
		return core.New(core.CodeLimitExceeded, core.StageScan, false,
			fmt.Sprintf("track count %d exceeds limit", len(traks)))
	}
	for _, trak := range traks {
		tr, err := s.readTrack(trak)
		if tr != nil {
			s.Tracks = append(s.Tracks, *tr)
		}
		if err != nil && first == nil {
			first = err
		}
		if err != nil && tr != nil {
			tr.Warnings = append(tr.Warnings, err)
		}
	}
	return first
}

func (s *Scan) limTracksExceeded(n int) bool {
	lim := s.Limits.MaxTracks
	if lim <= 0 {
		lim = 32
	}
	return n > lim
}

func (s *Scan) readTrack(trak *Box) (*TrackScan, *core.Error) {
	tr := &TrackScan{}
	f := s.root()
	if f == nil {
		return tr, core.New(core.CodeIO, core.StageScan, false, "container reopened")
	}

	mdia := trak.Find("mdia")
	if mdia == nil {
		return tr, core.New(core.CodeMissingTrack, core.StageScan, true, "trak has no mdia")
	}
	if tkhd, ok := findDirect(trak, "tkhd"); ok {
		var v mp4.Tkhd
		if err := decodeFile(f, tkhd, &v); err != nil {
			tr.Warnings = append(tr.Warnings, core.Wrap(core.CodeCorruptFragment, core.StageScan, true,
				"undecodable tkhd", err).WithOffset(tkhd.Offset))
		}
		tr.ID = v.TrackID
	}

	mdhdBox, ok := findDirect(mdia, "mdhd")
	if !ok {
		return tr, core.New(core.CodeMissingTrack, core.StageScan, true, "track has no mdhd").WithTrack(tr.ID, 0)
	}
	var mdhd mp4.Mdhd
	if err := decodeFile(f, mdhdBox, &mdhd); err != nil {
		return tr, core.Wrap(core.CodeCorruptFragment, core.StageScan, true,
			"undecodable mdhd", err).WithOffset(mdhdBox.Offset)
	}
	tr.Timescale = mdhd.Timescale
	tr.Duration = mdhd.GetDuration()
	if tr.Timescale == 0 {
		return tr, core.New(core.CodeBadTimestamp, core.StageScan, true,
			"track timescale is zero").WithTrack(tr.ID, 0)
	}

	if hdlrBox, ok := findDirect(mdia, "hdlr"); ok {
		var hdlr mp4.Hdlr
		if err := decodeFile(f, hdlrBox, &hdlr); err == nil {
			tr.Handler = string(hdlr.HandlerType[:])
			tr.IsAudio = tr.Handler == "soun"
		} else {
			tr.Warnings = append(tr.Warnings, core.Wrap(core.CodeCorruptFragment, core.StageScan, true,
				"undecodable hdlr", err).WithOffset(hdlrBox.Offset))
		}
	}

	if edts, ok := findDirect(trak, "edts"); ok {
		if elstBox, ok := findDirect(edts, "elst"); ok {
			var elst mp4.Elst
			if err := decodeFile(f, elstBox, &elst); err == nil && len(elst.Entries) > 0 {
				tr.EditShift = elst.GetMediaTime(0)
			} else if err != nil {
				tr.Warnings = append(tr.Warnings, core.Wrap(core.CodeCorruptFragment, core.StageScan, true,
					"undecodable elst", err).WithOffset(elstBox.Offset))
			}
		}
	}

	stbl := mdia.Find("stbl")
	if stbl == nil {
		return tr, core.New(core.CodeTableTruncated, core.StageScan, true,
			"track has no stbl").WithTrack(tr.ID, 0)
	}

	if stsd, ok := findDirect(stbl, "stsd"); ok {
		if ferr := s.readAudioFormat(f, stsd, tr); ferr != nil {
			tr.Warnings = append(tr.Warnings, ferr)
		}
	}
	if !tr.IsAudio && tr.Format.Codec == "" {
		tr.Note = "non-audio track ignored"
		return tr, nil
	}

	var firstErr *core.Error
	for _, step := range []struct {
		name string
		fn   func(*os.File, *Box, *TrackScan) *core.Error
	}{
		{"stts", readStts}, {"stsc", readStsc}, {"stsz", readStsz},
		{"stco", readStco}, {"co64", readCo64},
	} {
		box, ok := findDirect(stbl, step.name)
		if !ok {
			continue
		}
		if err := step.fn(f, box, tr); err != nil {
			tr.IndexComplete = false
			if firstErr == nil {
				firstErr = err
			}
			tr.Warnings = append(tr.Warnings, err)
		}
	}
	tr.IndexComplete = firstErr == nil && len(tr.Stts) > 0 && len(tr.Stsc) > 0 &&
		len(tr.Offsets) > 0 && tr.SampleCount() > 0
	return tr, firstErr
}
