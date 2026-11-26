// Package rewrap writes canonical MP4 containers without transcoding samples.
package rewrap

import (
	"bytes"
	"fmt"
	"math"

	"mediapipeline/pkg/media"
)

// Rewrap serializes all tracks and their original sample bytes. It preserves
// decode/composition timestamps, durations, and sync-sample markers.
func Rewrap(info *media.ContainerInfo) ([]byte, error) {
	if info == nil || len(info.Tracks) == 0 {
		return nil, fmt.Errorf("rewrap: no tracks")
	}
	ftyp := ftypBox()
	moovPlaceholder, err := buildMoov(info, true)
	if err != nil {
		return nil, err
	}
	dataStart := len(ftyp) + len(moovPlaceholder) + 8

	offsetsByTrack := make([][]uint32, len(info.Tracks))
	pos := dataStart
	for ti := range info.Tracks {
		offsets := make([]uint32, len(info.Tracks[ti].Samples))
		for si, s := range info.Tracks[ti].Samples {
			if uint64(pos) > math.MaxUint32 {
				return nil, fmt.Errorf("rewrap: chunk offset exceeds uint32; co64 required")
			}
			offsets[si] = uint32(pos)
			if int(s.Size) != len(s.Data) {
				return nil, fmt.Errorf("rewrap: track %d sample %d size/data mismatch", info.Tracks[ti].TrackID, s.Number)
			}
			pos += len(s.Data)
		}
		offsetsByTrack[ti] = offsets
	}

	moov, err := buildMoovWithOffsets(info, offsetsByTrack)
	if err != nil {
		return nil, err
	}
	if len(moov) != len(moovPlaceholder) {
		return nil, fmt.Errorf("rewrap: moov size changed while patching stco")
	}

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(moov)
	var mdat bytes.Buffer
	for _, t := range info.Tracks {
		for _, s := range t.Samples {
			mdat.Write(s.Data)
		}
	}
	out.Write(box("mdat", mdat.Bytes()))
	return out.Bytes(), nil
}

func buildMoov(info *media.ContainerInfo, placeholder bool) ([]byte, error) {
	offsets := make([][]uint32, len(info.Tracks))
	if placeholder {
		for i, t := range info.Tracks {
			offsets[i] = make([]uint32, len(t.Samples))
		}
	}
	return buildMoovWithOffsets(info, offsets)
}

func buildMoovWithOffsets(info *media.ContainerInfo, offsetsByTrack [][]uint32) ([]byte, error) {
	movieTB := info.MovieTimebase.Timescale
	if movieTB == 0 {
		movieTB = info.Tracks[0].Timebase.Timescale
	}
	movieDuration := info.MovieDuration
	if movieDuration == 0 {
		movieDuration = trackDuration(&info.Tracks[0])
	}

	var trakBytes bytes.Buffer
	nextTrackID := uint32(1)
	for i := range info.Tracks {
		t := &info.Tracks[i]
		if t.Timebase.Timescale == 0 {
			return nil, fmt.Errorf("rewrap: track %d has zero timescale", t.TrackID)
		}
		if len(offsetsByTrack) != len(info.Tracks) || offsetsByTrack[i] == nil {
			return nil, fmt.Errorf("rewrap: missing offsets for track %d", t.TrackID)
		}
		tb, err := buildTrak(t, offsetsByTrack[i], movieDuration)
		if err != nil {
			return nil, err
		}
		trakBytes.Write(tb)
		if t.TrackID >= nextTrackID {
			nextTrackID = t.TrackID + 1
		}
	}

	var moovPayload bytes.Buffer
	moovPayload.Write(mvhdBox(movieTB, movieDuration))
	moovPayload.Write(trakBytes.Bytes())
	return box("moov", moovPayload.Bytes()), nil
}

func buildTrak(t *media.TrackInfo, offsets []uint32, movieDuration uint64) ([]byte, error) {
	if len(offsets) != len(t.Samples) {
		return nil, fmt.Errorf("rewrap: track %d has %d offsets for %d samples", t.TrackID, len(offsets), len(t.Samples))
	}
	duration := t.DurationTicks
	if duration == 0 {
		duration = trackDuration(t)
	}

	var stbl bytes.Buffer
	stbl.Write(stsdBox())
	stbl.Write(sttsBox(t.Samples))
	if ctts := cttsBox(t.Samples); ctts != nil {
		stbl.Write(ctts)
	}
	stbl.Write(stscBox())
	stbl.Write(stszBox(t.Samples))
	if stss := stssBox(t.Samples); stss != nil {
		stbl.Write(stss)
	}
	stbl.Write(stcoBox(offsets))

	var minf bytes.Buffer
	minf.Write(fullBox("vmhd", 0, 1, make([]byte, 8)))
	var drefPayload bytes.Buffer
	drefPayload.Write(u32(1))
	drefPayload.Write(fullBox("url ", 0, 1, nil))
	minf.Write(box("dinf", fullBox("dref", 0, 0, drefPayload.Bytes())))
	minf.Write(box("stbl", stbl.Bytes()))

	var mdia bytes.Buffer
	mdia.Write(mdhdBox(t.Timebase.Timescale, duration))
	mdia.Write(hdlrBox())
	mdia.Write(box("minf", minf.Bytes()))

	elst := elstBox(t.EditList, t.Timebase.Timescale, duration)

	var trak bytes.Buffer
	trak.Write(tkhdBox(t.TrackID, movieDuration))
	trak.Write(box("edts", elst))
	trak.Write(box("mdia", mdia.Bytes()))
	return box("trak", trak.Bytes()), nil
}

func trackDuration(t *media.TrackInfo) uint64 {
	var d int64
	for _, s := range t.Samples {
		next, err := media.Advance(d, s.Duration)
		if err != nil {
			return ^uint64(0)
		}
		d = next
	}
	return uint64(d)
}
