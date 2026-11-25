package scan

import (
	"fmt"

	"github.com/abema/go-mp4"

	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/fault"
	"example.com/assetcheck/internal/index"
)

// ------------------------------------------------------------- fragments --

type trunEntry struct {
	duration uint32
	size     uint32
}

type trafPlan struct {
	trackID    uint32
	baseOffset *uint64
	moofBase   bool
	baseTime   *uint64
	truns      []trunPlan
}

type trunPlan struct {
	dataOffset *int32
	entries    []trunEntry
}

// parseMoof reads a moof, then materialises its samples once all per-track
// anchors (tfhd/tfdt/trun) are known.
func (s *Scanner) parseMoof(off, size int64) bool {
	fragIdx := len(s.idx.Fragments)
	s.idx.Fragments = append(s.idx.Fragments, index.Fragment{
		Ordinal: fragIdx,
		Offset:  off,
		Size:    size,
	})
	frag := &s.idx.Fragments[fragIdx]
	var trafs []trafPlan
	ok := s.walkChildren(off+int64(headerSize(s.data, off)), off+size, func(bi *mp4.BoxInfo) bool {
		switch bi.Type {
		case mp4.BoxTypeMfhd():
			var box mp4.Mfhd
			if err := s.decode(bi, &box); err != nil {
				s.fail(fault.Wrap(fault.StageScan, "scan.mfhd", int64(bi.Offset), clock.Invalid, "mfhd cannot be parsed", err))
				return false
			}
			frag.Sequence = box.SequenceNumber
		case mp4.BoxTypeTraf():
			tp, ok := s.parseTraf(bi)
			if !ok {
				return false
			}
			trafs = append(trafs, tp)
		}
		return true
	})
	if !ok {
		return false
	}
	return s.materialise(fragIdx, off, size, trafs)
}

func (s *Scanner) parseTraf(traf *mp4.BoxInfo) (trafPlan, bool) {
	tp := trafPlan{}
	ok := s.walkChildren(int64(traf.Offset)+headerSize(s.data, int64(traf.Offset)), int64(traf.Offset+traf.Size),
		func(bi *mp4.BoxInfo) bool {
			switch bi.Type {
			case mp4.BoxTypeTfhd():
				var box mp4.Tfhd
				if err := s.decode(bi, &box); err != nil {
					s.fail(fault.Wrap(fault.StageScan, "scan.tfhd", int64(bi.Offset), clock.Invalid, "tfhd cannot be parsed", err))
					return false
				}
				tp.trackID = box.TrackID
				if box.CheckFlag(0x000001) {
					v := box.BaseDataOffset
					tp.baseOffset = &v
				}
				if box.CheckFlag(0x020000) {
					tp.moofBase = true
				}
			case mp4.BoxTypeTfdt():
				var box mp4.Tfdt
				if err := s.decode(bi, &box); err != nil {
					s.fail(fault.Wrap(fault.StageScan, "scan.tfdt", int64(bi.Offset), clock.Invalid, "tfdt cannot be parsed", err))
					return false
				}
				v := box.GetBaseMediaDecodeTime()
				tp.baseTime = &v
			case mp4.BoxTypeTrun():
				var box mp4.Trun
				if err := s.decode(bi, &box); err != nil {
					s.fail(fault.Wrap(fault.StageScan, "scan.trun", int64(bi.Offset), clock.Invalid, "trun cannot be parsed", err))
					return false
				}
				tp.truns = append(tp.truns, convertTrun(&box))
			}
			return true
		})
	return tp, ok
}

func convertTrun(box *mp4.Trun) trunPlan {
	tp := trunPlan{}
	if box.CheckFlag(0x000001) {
		v := box.DataOffset
		tp.dataOffset = &v
	}
	for _, e := range box.Entries {
		tp.entries = append(tp.entries, trunEntry{duration: e.SampleDuration, size: e.SampleSize})
	}
	return tp
}

// materialise turns trun entries into absolute samples. Data offsets are
// resolved against the tfhd base data offset, the moof start (when
// default-base-is-moof or no base offset is declared) or the running cursor.
func (s *Scanner) materialise(fragIdx int, moofOff, moofSize int64, trafs []trafPlan) bool {
	frag := &s.idx.Fragments[fragIdx]
	moofEnd := moofOff + moofSize
	for _, tp := range trafs {
		info := s.track[tp.trackID]
		if info == nil {
			s.fail(fault.New(fault.StageScan, "scan.trun.unknown_track", moofOff, clock.Invalid,
				fmt.Sprintf("fragment references track %d which moov does not declare", tp.trackID)))
			return false
		}
		tr := s.trackView(tp.trackID)
		if tr == nil {
			return false
		}
		base := moofEnd
		if tp.baseOffset != nil {
			base = int64(*tp.baseOffset)
		}
		cursor := base
		startDTS := s.dts[tp.trackID]
		if tp.baseTime != nil {
			t, err := clock.FromUnits(*tp.baseTime, info.Timescale)
			if err != nil {
				s.fail(fault.Wrap(fault.StageScan, "scan.tfdt.overflow", moofOff, clock.Invalid,
					"base media decode time does not fit the time base", err))
				return false
			}
			startDTS = t
		}
		s.dts[tp.trackID] = startDTS
		first := len(tr.Samples)
		for _, run := range tp.truns {
			if run.dataOffset != nil {
				cursor = base + int64(*run.dataOffset)
			}
			for _, e := range run.entries {
				off := cursor
				if int64(e.size) <= 0 || off < 0 || off+int64(e.size) > int64(len(s.data)) {
					s.fail(fault.New(fault.StageScan, "scan.trun.sample_outside_input", off, s.dts[tp.trackID],
						fmt.Sprintf("track %d fragment %d sample %d spans bytes %d..%d but input is %d bytes",
							tp.trackID, fragIdx, len(tr.Samples)-first, off, off+int64(e.size), len(s.data))).
						WithSample(len(tr.Samples)))
					return false
				}
				dur, err := clock.FromUnits(uint64(e.duration), info.Timescale)
				if err != nil {
					s.fail(fault.Wrap(fault.StageScan, "scan.trun.bad_duration", off, clock.Invalid,
						"sample duration does not map onto the time base", err).WithSample(len(tr.Samples)))
					return false
				}
				if e.duration == 0 {
					s.fail(fault.New(fault.StageScan, "scan.trun.zero_duration", off, s.dts[tp.trackID],
						"sample declares a zero duration").WithSample(len(tr.Samples)))
					return false
				}
				tr.Samples = append(tr.Samples, index.Sample{
					TrackID:      tp.trackID,
					Index:        len(tr.Samples),
					Offset:       off,
					Size:         e.size,
					Ticks:        uint64(e.duration),
					TickDuration: e.duration,
					DTS:          s.dts[tp.trackID],
					Duration:     dur,
					Fragment:     fragIdx,
				})
				s.dts[tp.trackID] = clock.Add(s.dts[tp.trackID], dur)
				cursor += int64(e.size)
			}
		}
		if len(tr.Samples) > first {
			frag.Tracks = append(frag.Tracks, tp.trackID)
			frag.Samples += len(tr.Samples) - first
		}
	}
	s.updateFragmentRange(fragIdx)
	return true
}

// trackView returns the index track that mirrors s.track[id].
func (s *Scanner) trackView(id uint32) *index.Track {
	for i := range s.idx.Tracks {
		if s.idx.Tracks[i].ID == id {
			return &s.idx.Tracks[i]
		}
	}
	return nil
}

// updateFragmentRange recomputes the fragment time range from its samples.
func (s *Scanner) updateFragmentRange(fragIdx int) {
	frag := &s.idx.Fragments[fragIdx]
	frag.Start, frag.End = clock.Invalid, clock.Invalid
	for ti := range s.idx.Tracks {
		for si := range s.idx.Tracks[ti].Samples {
			smp := &s.idx.Tracks[ti].Samples[si]
			if smp.Fragment != fragIdx {
				continue
			}
			hi := clock.Add(smp.DTS, smp.Duration)
			if frag.Start == clock.Invalid || smp.DTS < frag.Start {
				frag.Start = smp.DTS
			}
			if frag.End == clock.Invalid || hi > frag.End {
				frag.End = hi
			}
		}
	}
}

// finalizeTracks derives each track's covered time range and confirms that
// decode times advance monotonically.
func (s *Scanner) finalizeTracks() {
	for ti := range s.idx.Tracks {
		tr := &s.idx.Tracks[ti]
		if len(tr.Samples) == 0 {
			s.fail(fault.New(fault.StageScan, "scan.track.no_samples", fault.NoOffset, clock.Invalid,
				fmt.Sprintf("track %d has no samples in any fragment", tr.ID)))
			continue
		}
		var prevEnd clock.Nanos
		for si := range tr.Samples {
			smp := &tr.Samples[si]
			if si > 0 && smp.DTS < prevEnd && smp.DTS < tr.Samples[si-1].DTS {
				s.fail(fault.New(fault.StageScan, "scan.track.non_monotonic", smp.Offset, smp.DTS,
					fmt.Sprintf("track %d sample %d goes backwards in decode time", tr.ID, si)).WithSample(si))
				break
			}
			prevEnd = clock.Add(smp.DTS, smp.Duration)
		}
		tr.Start = tr.Samples[0].DTS
		last := tr.Samples[len(tr.Samples)-1]
		tr.End = clock.Add(last.DTS, last.Duration)
	}
}

// --------------------------------------------------------------- packets --
