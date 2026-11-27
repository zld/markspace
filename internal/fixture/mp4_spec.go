package fixture

import (
	"mediapack/internal/binio"
)

// MP4TrackSpec describes one generated ISO-BMFF track.
type MP4TrackSpec struct {
	TrackID   uint32
	Timescale uint32
	Codec     string // fourCC
	Handler   string // vide | soun
	Rotation  int32  // degrees, 0/90/180/270
	CropL     uint32
	CropT     uint32
	CropR     uint32
	CropB     uint32
	ColorPrim uint16
	ColorTRC  uint16
	ColorMx   uint16
	FullRange bool
	Count     int
	Size      int
}

// MP4Sample is the ground-truth view of one generated MP4 sample.
type MP4Sample struct {
	TrackID  uint32
	Index    uint32
	DTS      int64
	Duration int64
	Offset   int64 // mdat payload offset in the file
	Size     uint32
}

type mp4Builder struct {
	buf []byte
}

// laid is a track's payloads and their resolved mdat byte offsets.
type laid struct {
	spec    MP4TrackSpec
	payload [][]byte
	offsets []int64
}

func (b *mp4Builder) box(typ string, payload []byte) {
	off, _, err := binio.Box(&sliceWriter{&b.buf}, typ, payload)
	if err != nil {
		panic(err)
	}
	_ = off
}

// sliceWriter implements io.WriteSeeker over a growing byte slice.
type sliceWriter struct{ p *[]byte }

func (w *sliceWriter) Write(p []byte) (int, error) {
	*w.p = append(*w.p, p...)
	return len(p), nil
}
func (w *sliceWriter) Seek(off int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case 0:
		base = 0
	case 1:
		base = int64(len(*w.p))
	case 2:
		base = int64(len(*w.p))
	}
	return base + off, nil
}

// MP4 builds a deterministic minimal ISO-BMFF file. Samples are one per chunk
// (stsc), 32-bit chunk offsets (stco), uniform size (stsz) and one stts run.
func buildMP4(tracks []MP4TrackSpec) ([]byte, []MP4Sample, error) {
	b := &mp4Builder{}
	// ftyp
	ftyp := append([]byte("isom"), 0, 0, 0, 0)
	ftyp = append(ftyp, []byte("isomiso2")...)
	b.box("ftyp", ftyp)

	// Compute sample payloads and their eventual mdat offsets up front, since
	// stco must point at them and moov precedes mdat in this layout.
	layouts := make([]laid, len(tracks))
	mdatHeader := 8 // box header for the single shared mdat
	mdatPayloadSize := 0
	for ti, tr := range tracks {
		l := laid{spec: tr}
		for i := 0; i < tr.Count; i++ {
			p := samplePayload("mp4", tr.TrackID, uint32(i), tr.Size)
			l.payload = append(l.payload, p)
			mdatPayloadSize += len(p)
		}
		layouts[ti] = l
	}
	// moov size is needed to know where mdat payload starts.
	moov := buildMoov(tracks)
	mdatPayloadStart := int64(8 /*ftyp*/ + len(ftyp) + len(moov) + mdatHeader)

	var ground []MP4Sample
	cursor := mdatPayloadStart
	for ti := range layouts {
		l := &layouts[ti]
		for i, p := range l.payload {
			l.offsets = append(l.offsets, cursor)
			tr := l.spec
			dts := int64(i) * int64(tr.Timescale) / 25
			ground = append(ground, MP4Sample{
				TrackID: tr.TrackID, Index: uint32(i), DTS: dts,
				Duration: int64(tr.Timescale / 25), Offset: cursor,
				Size: uint32(len(p)),
			})
			cursor += int64(len(p))
		}
	}
	b.buf = append(b.buf, moov...)

	// mdat with payloads in track order.
	mdat := make([]byte, 0, mdatPayloadSize)
	for _, l := range layouts {
		for _, p := range l.payload {
			mdat = append(mdat, p...)
		}
	}
	b.box("mdat", mdat)

	// Patch stco offsets now that the file layout is fixed. The offsets were
	// computed from the same layout, so assert rather than guess.
	if err := patchStco(b.buf, layouts, mdatPayloadStart); err != nil {
		return nil, nil, err
	}
	return b.buf, ground, nil
}
