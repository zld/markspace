package media

import (
	"fmt"
)

// trackPayload is the deterministic description of a fixture track.
type trackPayload struct {
	ID        uint32
	Codec     string
	Lang      [3]byte
	Timescale uint32
	Durs      []uint32
	Offsets   []int64
	Sizes     []uint32
	SyncIdx   []uint32 // 1-based sample numbers
	Edits     []EditEntry
	Width     uint16
	Height    uint16
	Channels  uint16
	Rate      uint32
}

func (t *trackPayload) duration() uint64 {
	var d uint64
	for _, x := range t.Durs {
		d += uint64(x)
	}
	return d
}

// buildTrak encodes one trak box. chunkOffsets are absolute file offsets of
// each sample's bytes in mdat.
func buildTrak(t *trackPayload, movieTS uint32, chunkOffsets []uint32) []byte {
	var children []byte
	// duration in movie timescale
	mdur, _ := RescaleRound(t.duration(), t.Timescale, movieTS)
	children = append(children, tkhdBox(t.ID, mdur, t.Width, t.Height, t.Codec == "mp4a", 0)...)
	if len(t.Edits) > 0 {
		children = append(children, box("edts", elstBox(t.Edits))...)
	}
	var mdia []byte
	mdia = append(mdia, mdhdBox(t.Timescale, t.duration(), t.Lang, 0)...)
	if t.Codec == "mp4a" {
		mdia = append(mdia, hdlrBox("soun", "SoundHandler")...)
	} else {
		mdia = append(mdia, hdlrBox("vide", "VideoHandler")...)
	}
	var minf []byte
	if t.Codec == "mp4a" {
		minf = append(minf, smhdBox()...)
	} else {
		minf = append(minf, vmhdBox()...)
	}
	minf = append(minf, dinfBox()...)
	var stbl []byte
	stbl = append(stbl, stsdBox(t.Codec, t.Width, t.Height, t.Channels, t.Rate)...)
	stbl = append(stbl, sttsBox(t.Durs)...)
	hasCTTS := false
	for _, o := range t.Offsets {
		if o != 0 {
			hasCTTS = true
			break
		}
	}
	if hasCTTS {
		stbl = append(stbl, cttsBox(t.Offsets)...)
	}
	// one sample per chunk keeps offsets exact and trivially verifiable
	stbl = append(stbl, stscBox([][3]uint32{{1, 1, 1}})...)
	stbl = append(stbl, stszBox(t.Sizes)...)
	stbl = append(stbl, stcoBox(chunkOffsets)...)
	if len(t.SyncIdx) > 0 && len(t.SyncIdx) != len(t.Sizes) {
		stbl = append(stbl, stssBox(t.SyncIdx)...)
	}
	minf = append(minf, box("stbl", stbl)...)
	mdia = append(mdia, box("minf", minf)...)
	children = append(children, box("mdia", mdia)...)
	return box("trak", children)
}

// BuildFile assembles a complete MP4/MOV file from track payloads. samples is
// indexed by track, then by sample in decode order.
func BuildFile(brand string, movieTS uint32, tracks []*trackPayload, samples [][][]byte) ([]byte, error) {
	if len(tracks) != len(samples) {
		return nil, fmt.Errorf("tracks/samples length mismatch")
	}
	var compat [][4]byte
	if brand == "qt  " {
		compat = [][4]byte{{'q', 't', ' ', ' '}}
	} else {
		compat = [][4]byte{{'i', 's', 'o', 'm'}, {'i', 's', 'o', '2'}, {'a', 'v', 'c', '1'}, {'m', 'p', '4', '1'}}
	}
	ft := &enc{}
	ft.fourcc(brand).u32(0x00000200)
	for _, c := range compat {
		ft.bytes(c[:])
	}
	ftyp := box("ftyp", ft.done())

	total := 0
	perTrackSizes := make([][]uint32, len(tracks))
	for ti, s := range samples {
		for _, b := range s {
			total += len(b)
			perTrackSizes[ti] = append(perTrackSizes[ti], uint32(len(b)))
		}
		if len(s) == 0 {
			return nil, fmt.Errorf("track %d has no samples", ti)
		}
	}
	mdatPayload := make([]byte, 0, total)
	for _, s := range samples {
		for _, b := range s {
			mdatPayload = append(mdatPayload, b...)
		}
	}

	// Track sample bytes are contiguous in mdat, in track order.
	buildMoov := func(mdatStart int) []byte {
		var traks []byte
		cursor := mdatStart
		trackCursor := make([]int, len(tracks))
		for i := range trackCursor {
			trackCursor[i] = cursor
			for _, n := range perTrackSizes[i] {
				cursor += int(n)
			}
		}
		for ti, t := range tracks {
			offs := make([]uint32, len(perTrackSizes[ti]))
			for k, n := range perTrackSizes[ti] {
				offs[k] = uint32(trackCursor[ti])
				trackCursor[ti] += int(n)
			}
			traks = append(traks, buildTrak(t, movieTS, offs)...)
		}
		var maxDur uint64
		for _, t := range tracks {
			d, _ := RescaleRound(t.duration(), t.Timescale, movieTS)
			if d > maxDur {
				maxDur = d
			}
		}
		mv := mvhdBox(movieTS, maxDur, uint32(len(tracks)+1), 0)
		return box("moov", append(mv, traks...))
	}

	guess := len(ftyp) + 4096
	moov := buildMoov(guess)
	mdatStart := len(ftyp) + len(moov) + 8
	moov = buildMoov(mdatStart)
	if len(ftyp)+len(moov)+8 != mdatStart {
		return nil, fmt.Errorf("moov size changed between passes")
	}
	mdat := box("mdat", mdatPayload)
	out := make([]byte, 0, len(ftyp)+len(moov)+len(mdat))
	out = append(out, ftyp...)
	out = append(out, moov...)
	out = append(out, mdat...)
	return out, nil
}
