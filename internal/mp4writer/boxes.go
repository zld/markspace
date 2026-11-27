package mp4writer

import (
	"github.com/abema/go-mp4"

	"media-check/internal/fixture"
	"media-check/internal/stream"
)

// boxBuilder accumulates nested boxes against a fresh in-memory writer.
type boxBuilder struct {
	ws *memSeeker
	w  *mp4.Writer
}

func newBuilder() *boxBuilder {
	ws := &memSeeker{}
	return &boxBuilder{ws: ws, w: mp4.NewWriter(ws)}
}

// start opens a container/leaf box.
func (b *boxBuilder) start(t mp4.BoxType) error {
	_, err := b.w.StartBox(&mp4.BoxInfo{Type: t})
	return err
} // payload marshals a structured box body.
func (b *boxBuilder) payload(box mp4.IImmutableBox) error {
	_, err := mp4.Marshal(b.w, box, mp4.Context{})
	return err
}

func (b *boxBuilder) end() error { _, err := b.w.EndBox(); return err }

func (b *boxBuilder) bytes() []byte { return b.ws.bytes() }

func buildFtyp() ([]byte, error) {
	b := newBuilder()
	if err := b.start(mp4.BoxTypeFtyp()); err != nil {
		return nil, err
	}
	err := b.payload(&mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '2'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	})
	if err != nil {
		return nil, err
	}
	if err := b.end(); err != nil {
		return nil, err
	}
	return b.bytes(), nil
}

// buildMoovBytes produces the moov box tree using one chunk offset per track.
func buildMoovBytes(tracks []TrackInput, offsets []uint32) ([]byte, error) {
	b := newBuilder()
	if err := b.start(mp4.BoxTypeMoov()); err != nil {
		return nil, err
	}
	// mvhd: 1000-unit movie timescale spanning the longest track duration.
	maxDur := uint32(0)
	for _, t := range tracks {
		d := trackDurationMovie(t)
		if d > maxDur {
			maxDur = d
		}
	}
	if err := b.start(mp4.BoxTypeMvhd()); err != nil {
		return nil, err
	}
	if err := b.payload(&mp4.Mvhd{
		Timescale:   1000,
		DurationV0:  maxDur,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix,
		NextTrackID: uint32(len(tracks) + 1),
	}); err != nil {
		return nil, err
	}
	if err := b.end(); err != nil {
		return nil, err
	}

	for i, tk := range tracks {
		if err := writeTrak(b, tk, uint32(i+1), offsets[i]); err != nil {
			return nil, err
		}
	}
	if err := b.end(); err != nil { // moov
		return nil, err
	}
	return b.bytes(), nil
}

// trackDurationMovie converts a track's sample count into milliseconds.
func trackDurationMovie(t TrackInput) uint32 {
	tr := t.Report
	if len(tr.Samples) == 0 {
		return 0
	}
	span := tr.SampleTimeRange[1].Sub(tr.SampleTimeRange[0])
	// Add one sample period so the last sample's own duration is included.
	return uint32(span.Millis()) + uint32(1000/expectedFPS(tr))
}

func expectedFPS(tr *stream.TrackReport) int {
	if tr.Kind == fixture.TrackVideo {
		return 30
	}
	return 20
}

// writeTrak writes one trak subtree, patching its single chunk offset.
func writeTrak(b *boxBuilder, tk TrackInput, trackID uint32, chunkOffset uint32) error {
	tr := tk.Report
	if err := b.start(mp4.BoxTypeTrak()); err != nil {
		return err
	}
	// tkhd
	duration := trackDurationMovie(tk)
	if err := b.start(mp4.BoxTypeTkhd()); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{
		TrackID:    trackID,
		DurationV0: duration,
		Volume:     0,
		Matrix:     identityMatrix,
	}
	if tr.Kind == fixture.TrackAudio {
		tkhd.Volume = 0x0100
	}
	if tr.Kind == fixture.TrackVideo {
		tkhd.Width = uint32(tk.Width) << 16
		tkhd.Height = uint32(tk.Height) << 16
	}
	if err := b.payload(tkhd); err != nil {
		return err
	}
	if err := b.end(); err != nil {
		return err
	}
	// mdia
	if err := b.start(mp4.BoxTypeMdia()); err != nil {
		return err
	}
	if err := writeMdhd(b, tk, duration); err != nil {
		return err
	}
	if err := writeHdlr(b, tr.Kind); err != nil {
		return err
	}
	if err := b.start(mp4.BoxTypeMinf()); err != nil {
		return err
	}
	if tr.Kind == fixture.TrackVideo {
		if err := writeVmhd(b); err != nil {
			return err
		}
	} else {
		if err := writeSmhd(b); err != nil {
			return err
		}
	}
	if err := writeDinf(b); err != nil {
		return err
	}
	if err := writeStbl(b, tk, chunkOffset); err != nil {
		return err
	}
	if err := b.end(); err != nil { // minf
		return err
	}
	if err := b.end(); err != nil { // mdia
		return err
	}
	return b.end() // trak
}

func writeMdhd(b *boxBuilder, tk TrackInput, movieDur uint32) error {
	if err := b.start(mp4.BoxTypeMdhd()); err != nil {
		return err
	}
	// Track duration in its own timescale equals movie ms scaled by rate.
	rate := tk.Report.ClockRate
	md := &mp4.Mdhd{
		Timescale:  rate,
		DurationV0: uint32(int64(movieDur) * int64(rate) / 1000),
		Language:   [3]byte{'u', 'n', 'd'},
	}
	if err := b.payload(md); err != nil {
		return err
	}
	return b.end()
}

func writeHdlr(b *boxBuilder, kind fixture.TrackKind) error {
	if err := b.start(mp4.BoxTypeHdlr()); err != nil {
		return err
	}
	htype := [4]byte{'v', 'i', 'd', 'e'}
	name := "VideoHandler"
	if kind == fixture.TrackAudio {
		htype = [4]byte{'s', 'o', 'u', 'n'}
		name = "SoundHandler"
	}
	if err := b.payload(&mp4.Hdlr{HandlerType: htype, Name: name}); err != nil {
		return err
	}
	return b.end()
}

func writeVmhd(b *boxBuilder) error {
	if err := b.start(mp4.BoxTypeVmhd()); err != nil {
		return err
	}
	if err := b.payload(&mp4.Vmhd{}); err != nil {
		return err
	}
	return b.end()
}

func writeSmhd(b *boxBuilder) error {
	if err := b.start(mp4.BoxTypeSmhd()); err != nil {
		return err
	}
	if err := b.payload(&mp4.Smhd{}); err != nil {
		return err
	}
	return b.end()
}

func writeDinf(b *boxBuilder) error {
	if err := b.start(mp4.BoxTypeDinf()); err != nil {
		return err
	}
	if err := b.start(mp4.BoxTypeDref()); err != nil {
		return err
	}
	if err := b.payload(&mp4.Dref{EntryCount: 1}); err != nil {
		return err
	}
	if err := b.start(mp4.BoxTypeUrl()); err != nil {
		return err
	}
	if err := b.payload(&mp4.Url{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}); err != nil {
		return err
	}
	if err := b.end(); err != nil { // url
		return err
	}
	if err := b.end(); err != nil { // dref
		return err
	}
	return b.end() // dinf
}
