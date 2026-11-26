package mp4

import (
	go_mp4 "github.com/abema/go-mp4"

	"regress/internal/media"
)

// scanTrak parses one trak box into a TrackState. Any failure is returned as a
// structured media.Error so the caller can isolate this track.
func scanTrak(r readSeeker, source string, trak *boxHeader) (*TrackState, error) {
	mdia, err := findChild(r, trak, go_mp4.BoxTypeMdia())
	if err != nil {
		return nil, asTrackErr(source, err)
	}
	if mdia == nil {
		return nil, media.CorruptBox(source, int64(trak.offset), "trak has no mdia")
	}
	mdhdBox, err := findChild(r, mdia, go_mp4.BoxTypeMdhd())
	if err != nil {
		return nil, asTrackErr(source, err)
	}
	if mdhdBox == nil {
		return nil, media.CorruptBox(source, int64(trak.offset), "mdia has no mdhd")
	}
	var mdhd go_mp4.Mdhd
	if err := unmarshalBox(r, mdhdBox, &mdhd); err != nil {
		return nil, media.CorruptBox(source, int64(mdhdBox.offset), "mdhd unmarshal failed: "+err.Error())
	}
	if mdhd.Timescale == 0 {
		return nil, media.CorruptBox(source, int64(mdhdBox.offset), "mdhd timescale is zero")
	}
	ts := &TrackState{
		Timescale:     mdhd.Timescale,
		DurationTicks: mdhd.GetDuration(),
	}
	dur, err := media.MediaTime(int64(ts.DurationTicks), int64(ts.Timescale))
	if err != nil {
		return nil, asTrackErr(source, err)
	}
	ts.Duration = dur

	minf, err := findChild(r, mdia, go_mp4.BoxTypeMinf())
	if err != nil {
		return nil, asTrackErr(source, err)
	}
	stbl := (*boxHeader)(nil)
	if minf != nil {
		stbl, err = findChild(r, minf, go_mp4.BoxTypeStbl())
		if err != nil {
			return nil, asTrackErr(source, err)
		}
	}
	if stbl == nil {
		return nil, media.CorruptBox(source, int64(trak.offset), "track has no stbl")
	}
	if err := fillSamples(r, source, stbl, ts); err != nil {
		return nil, err
	}
	fillTrackID(r, source, trak, ts)
	fillBoundaries(ts)
	ts.ContainerDigest = media.DigestBytes(media.KindContainer,
		[]byte(boxPathDigest(r, trak)))
	return ts, nil
}

// readSeeker is the local interface used across scan helpers.
type readSeeker = interface {
	Read(p []byte) (int, error)
	Seek(offset int64, whence int) (int64, error)
}

func fillTrackID(r readSeeker, source string, trak *boxHeader, ts *TrackState) {
	tkhdBox, err := findChild(r, trak, go_mp4.BoxTypeTkhd())
	if err != nil || tkhdBox == nil {
		return
	}
	var tkhd go_mp4.Tkhd
	if err := unmarshalBox(r, tkhdBox, &tkhd); err == nil {
		ts.TrackID = tkhd.TrackID
	}
}

func fillBoundaries(ts *TrackState) {
	if ts.SampleCount == 0 {
		ts.Start = Boundary{Tick: 0, Time: media.NewInt(0)}
		ts.End = Boundary{Tick: ts.DurationTicks, Time: ts.Duration}
		return
	}
	ts.Start = Boundary{Tick: 0, Time: media.NewInt(0)}
	var lastTick uint64
	for _, s := range ts.Samples {
		lastTick += uint64(s.Duration)
	}
	t, _ := media.MediaTime(int64(lastTick), int64(ts.Timescale))
	ts.End = Boundary{Tick: lastTick, Time: t}
}

func boxPathDigest(r readSeeker, h *boxHeader) string {
	kids, err := childBoxes(r, h)
	if err != nil {
		return h.typ.String()
	}
	out := h.typ.String()
	for _, k := range kids {
		out += "/" + boxPathDigest(r, k)
	}
	return out
}

func asTrackErr(source string, err error) error {
	if err == nil {
		return nil
	}
	if me, ok := media.AsError(err); ok {
		if me.Source == "input" || me.Source == "time" {
			me.Source = source
		}
		return me
	}
	return media.CorruptBox(source, -1, err.Error())
}
