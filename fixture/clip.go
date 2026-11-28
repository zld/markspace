package fixture

import "bytes"

// TrackSpec describes one track to be written into a generated clip.
type TrackSpec struct {
	ID          uint32
	Handler     string // vide, soun, subt
	Timescale   uint32
	Width       uint16
	Height      uint16
	Volume      uint16
	Language    string
	SampleEntry []byte
	Samples     []Sample
}

// DurationTicks is the total media duration of the track.
func (t TrackSpec) DurationTicks() uint32 {
	var d uint32
	for _, s := range t.Samples {
		d += s.DurationTicks
	}
	return d
}

// Clip is an assembled MP4 file split into a header (ftyp+moov) and the mdat
// payload, so callers can insert or edit boxes between them.
type Clip struct {
	Header []byte // ftyp + moov
	Data   []byte // mdat payload (no header)
}

// Bytes renders the complete file. moov is placed before mdat (fast start), so
// chunk offsets are resolvable in one pass.
func (c Clip) Bytes() []byte {
	var out bytes.Buffer
	out.Write(c.Header)
	out.Write(Box("mdat", c.Data))
	return out.Bytes()
}

// BuildClip assembles a fast-start MP4 from the given tracks.
func BuildClip(tracks []TrackSpec) Clip {
	// moov is built first to learn its size; the first sample byte lands right
	// after ftyp + moov + the 8-byte mdat header.
	// Iterate once with placeholder offsets, measure, then rebuild.
	build := func(firstDataOffset uint32) []byte {
		return buildMoov(tracks, firstDataOffset)
	}
	moov := build(0)
	ftyp := Ftyp("isom")
	// mdat payload length does not affect the offset, only moov's own size.
	firstDataOffset := uint32(len(ftyp) + len(moov) + 8)
	moov = build(firstDataOffset)
	// Recompute in case the offset width changed box sizes (it does not: stco
	// entries are fixed 4 bytes), but assert stability.
	if got := uint32(len(ftyp) + len(moov) + 8); got != firstDataOffset {
		firstDataOffset = got
		moov = build(firstDataOffset)
	}
	var data []byte
	for _, t := range tracks {
		for _, s := range t.Samples {
			data = append(data, s.Data...)
		}
	}
	return Clip{Header: append(ftyp, moov...), Data: data}
}

func buildMoov(tracks []TrackSpec, firstDataOffset uint32) []byte {
	var maxDuration uint32
	var movieTimescale uint32 = 1000
	for _, t := range tracks {
		if d := t.DurationTicks(); d > maxDuration {
			maxDuration = d
		}
	}
	_ = movieTimescale
	// mvhd is expressed in the first track's timescale-independent millisecond
	// clock; use a fixed 1000 tick/s movie clock for readability.
	movieDuration := uint32(0)
	for _, t := range tracks {
		if t.Timescale == 0 {
			continue
		}
		ms := uint64(t.DurationTicks()) * 1000 / uint64(t.Timescale)
		if uint32(ms) > movieDuration {
			movieDuration = uint32(ms)
		}
	}
	parts := [][]byte{Mvhd(1000, movieDuration)}
	cursor := firstDataOffset
	for _, t := range tracks {
		st := &SampleTable{Entries: t.Samples, Timescale: t.Timescale}
		var stsdEntry []byte
		switch t.Handler {
		case "vide":
			stsdEntry = t.SampleEntry
			if stsdEntry == nil {
				stsdEntry = Avc1SampleEntry(t.Width, t.Height, AvcC())
			}
		case "soun":
			stsdEntry = t.SampleEntry
			if stsdEntry == nil {
				stsdEntry = Mp4aSampleEntry(2, 48000, Esds())
			}
		default:
			stsdEntry = t.SampleEntry
			if stsdEntry == nil {
				stsdEntry = Tx3gSampleEntry()
			}
		}
		stbl := BuildStbl(st, stsdEntry, cursor)
		var minfHdr []byte
		if t.Handler == "vide" {
			minfHdr = Vmhd()
		} else {
			minfHdr = Smhd()
		}
		minf := Box("minf", minfHdr, Dinf(), stbl)
		mdia := Box("mdia", Mdhd(t.Timescale, t.DurationTicks(), langOr(t.Language, "und")), Hdlr(t.Handler, handlerName(t.Handler)), minf)
		trak := Box("trak", Tkhd(t.ID, t.DurationTicks(), t.Width, t.Height, t.Volume), mdia)
		parts = append(parts, trak)
		for _, s := range t.Samples {
			cursor += uint32(len(s.Data))
		}
	}
	return Box("moov", parts...)
}

func langOr(l, fallback string) string {
	if l == "" {
		return fallback
	}
	return l
}

func handlerName(h string) string {
	switch h {
	case "vide":
		return "VideoHandler"
	case "soun":
		return "SoundHandler"
	default:
		return "SubtitleHandler"
	}
}
