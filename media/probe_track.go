package media

import (
	"io"

	mp4 "github.com/abema/go-mp4"
)

// probeTrack extracts the typed payloads of one trak box.
func probeTrack(r io.ReadSeeker, bi *mp4.BoxInfo) (*TrackInfo, error) {
	paths := []mp4.BoxPath{
		{mp4.BoxTypeTkhd()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAvc1()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAvc1(), mp4.BoxTypeColr()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeMp4a()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStts()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStss()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsz()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStco()},
	}
	bips, err := mp4.ExtractBoxesWithPayload(r, bi, paths)
	if err != nil {
		off, _ := r.Seek(0, io.SeekCurrent)
		return nil, &Error{Kind: KindTruncatedBox, Op: "probe.track",
			Offset: off, HasOffset: true,
			Msg: "sample table unreadable", Cause: err}
	}

	t := &TrackInfo{}
	var mdhdOffset int64
	var mdhd *mp4.Mdhd
	for _, bip := range bips {
		switch bip.Info.Type {
		case mp4.BoxTypeTkhd():
			tkhd := bip.Payload.(*mp4.Tkhd)
			t.TrackID = int(tkhd.TrackID)
		case mp4.BoxTypeMdhd():
			mdhd = bip.Payload.(*mp4.Mdhd)
			mdhdOffset = int64(bip.Info.Offset)
		case mp4.BoxTypeHdlr():
			t.Handler = string(bip.Payload.(*mp4.Hdlr).HandlerType[:])
		case mp4.BoxTypeAvc1():
			vse := bip.Payload.(*mp4.VisualSampleEntry)
			t.Width, t.Height = vse.Width, vse.Height
		case mp4.BoxTypeColr():
			colr := bip.Payload.(*mp4.Colr)
			if string(colr.ColourType[:]) == "nclx" {
				t.Color = ColorMeta{Present: true,
					Primaries: colr.ColourPrimaries,
					Transfer:  colr.TransferCharacteristics,
					Matrix:    colr.MatrixCoefficients,
					FullRange: colr.FullRangeFlag}
			}
		case mp4.BoxTypeMp4a():
			ase := bip.Payload.(*mp4.AudioSampleEntry)
			t.ChannelCount = ase.ChannelCount
			t.SampleRate = ase.SampleRate >> 16 // 16.16 fixed point
		case mp4.BoxTypeStts():
			for _, e := range bip.Payload.(*mp4.Stts).Entries {
				t.Stts = append(t.Stts, SttsEntry{Count: e.SampleCount, Delta: e.SampleDelta})
				t.SampleCount += e.SampleCount
			}
		case mp4.BoxTypeStss():
			t.KeyframeNums = bip.Payload.(*mp4.Stss).SampleNumber
		case mp4.BoxTypeStsz():
			stsz := bip.Payload.(*mp4.Stsz)
			if stsz.SampleSize != 0 {
				t.SampleSizes = make([]uint32, stsz.SampleCount)
				for i := range t.SampleSizes {
					t.SampleSizes[i] = stsz.SampleSize
				}
			} else {
				t.SampleSizes = stsz.EntrySize
			}
		case mp4.BoxTypeStco():
			for _, o := range bip.Payload.(*mp4.Stco).ChunkOffset {
				t.ChunkOffsets = append(t.ChunkOffsets, uint64(o))
			}
		}
	}

	if mdhd == nil {
		return nil, &Error{Kind: KindTruncatedBox, Op: "probe.track",
			TrackID: t.TrackID, Msg: "mdhd box missing"}
	}
	if mdhd.Timescale == 0 {
		return nil, &Error{Kind: KindBadTimescale, Op: "probe.track",
			TrackID: t.TrackID, Offset: mdhdOffset, HasOffset: true,
			Msg: "mdhd timescale is zero"}
	}
	t.Timescale = mdhd.Timescale
	t.DurationUnits = mdhd.GetDuration()
	dur, err := Scale(t.DurationUnits, t.Timescale)
	if err != nil {
		if me, ok := err.(*Error); ok {
			me.TrackID = t.TrackID
			me.Offset = mdhdOffset
			me.HasOffset = true
		}
		return nil, err
	}
	t.DurationNanos = dur
	if t.TrackID == 0 {
		return nil, &Error{Kind: KindTruncatedBox, Op: "probe.track",
			Msg: "tkhd box missing"}
	}
	return t, nil
}
