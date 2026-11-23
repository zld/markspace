package demux

import (
	"github.com/abema/go-mp4"
)

func writeVideoTrak(w *mp4.Writer, ctx mp4.Context, spec FixtureSpec, dataBytes uint64) error {
	s := spec.Video
	trak, err := startChild(w, ctx, mp4.BoxTypeTrak())
	if err != nil {
		return err
	}
	dur := uint64(s.FrameCount) * uint64(s.FrameDelta)
	if err := writeFullBox(w, trak.Context, mp4.BoxTypeTkhd(), &mp4.Tkhd{
		TrackID: 1, DurationV0: uint32(dur), Volume: 0,
		Matrix: identityMatrix, Width: uint32(s.Width) << 16, Height: uint32(s.Height) << 16,
	}); err != nil {
		return err
	}
	mdia, err := startChild(w, trak.Context, mp4.BoxTypeMdia())
	if err != nil {
		return err
	}
	if err := writeFullBox(w, mdia.Context, mp4.BoxTypeMdhd(),
		&mp4.Mdhd{Timescale: s.Timescale, DurationV0: uint32(dur), Language: [3]byte{0x55, 0xC4, 0}}); err != nil {
		return err
	}
	if err := writeFullBox(w, mdia.Context, mp4.BoxTypeHdlr(),
		&mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "vide"}); err != nil {
		return err
	}
	minf, err := startChild(w, mdia.Context, mp4.BoxTypeMinf())
	if err != nil {
		return err
	}
	if err := writeFullBox(w, minf.Context, mp4.BoxTypeVmhd(), &mp4.Vmhd{}); err != nil {
		return err
	}
	if err := writeDinf(minf, w); err != nil {
		return err
	}
	stbl, err := startChild(w, minf.Context, mp4.BoxTypeStbl())
	if err != nil {
		return err
	}
	if err := writeVideoStsd(w, stbl, spec); err != nil {
		return err
	}
	if err := writeCommonTables(w, stbl, s.FrameCount, s.FrameDelta, dataBytes, 64); err != nil {
		return err
	}
	if err := writeStss(w, stbl, s.FrameCount, s.KeyframeEvery, spec.Fault); err != nil {
		return err
	}
	if err := writeMdst(w, stbl, 1, spec.Audio.Channels, 0, videoColorSrc(spec), colorPrimaries(spec)); err != nil {
		return err
	}
	if err := endBoxes(w, 3); err != nil { // stbl, minf, mdia
		return err
	}
	_, err = w.EndBox() // trak
	return err
}

func writeAudioTrak(w *mp4.Writer, ctx mp4.Context, spec FixtureSpec, dataBytes uint64) error {
	s := spec.Audio
	trak, err := startChild(w, ctx, mp4.BoxTypeTrak())
	if err != nil {
		return err
	}
	dur := uint64(s.FrameCount) * uint64(s.FrameSamples)
	if err := writeFullBox(w, trak.Context, mp4.BoxTypeTkhd(), &mp4.Tkhd{
		TrackID: 2, DurationV0: uint32(dur), Volume: 0x0100, Matrix: identityMatrix,
	}); err != nil {
		return err
	}
	mdia, err := startChild(w, trak.Context, mp4.BoxTypeMdia())
	if err != nil {
		return err
	}
	if err := writeFullBox(w, mdia.Context, mp4.BoxTypeMdhd(),
		&mp4.Mdhd{Timescale: s.Timescale, DurationV0: uint32(dur), Language: [3]byte{0x55, 0xC4, 0}}); err != nil {
		return err
	}
	if err := writeFullBox(w, mdia.Context, mp4.BoxTypeHdlr(),
		&mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "soun"}); err != nil {
		return err
	}
	minf, err := startChild(w, mdia.Context, mp4.BoxTypeMinf())
	if err != nil {
		return err
	}
	if err := writeFullBox(w, minf.Context, mp4.BoxTypeSmhd(), &mp4.Smhd{}); err != nil {
		return err
	}
	if err := writeDinf(minf, w); err != nil {
		return err
	}
	stbl, err := startChild(w, minf.Context, mp4.BoxTypeStbl())
	if err != nil {
		return err
	}
	if err := writeAudioStsd(w, stbl, s); err != nil {
		return err
	}
	if err := writeCommonTables(w, stbl, s.FrameCount, s.FrameSamples, dataBytes, 32); err != nil {
		return err
	}
	layout := s.Channels
	if spec.Fault == FaultBadLayout {
		layout = 255
	}
	if err := writeMdst(w, stbl, 2, layout, s.SampleRate, 0, 0); err != nil {
		return err
	}
	if err := endBoxes(w, 3); err != nil { // stbl, minf, mdia
		return err
	}
	_, err = w.EndBox() // trak
	return err
}

// endBoxes closes n currently open container boxes in order.
func endBoxes(w *mp4.Writer, n int) error {
	for i := 0; i < n; i++ {
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	return nil
}

func writeDinf(minf *mp4.BoxInfo, w *mp4.Writer) error {
	dinf, err := startChild(w, minf.Context, mp4.BoxTypeDinf())
	if err != nil {
		return err
	}
	dref, err := startChild(w, dinf.Context, mp4.BoxTypeDref())
	if err != nil {
		return err
	}
	if err := marshalBox(w, dref, &mp4.Dref{EntryCount: 1}); err != nil {
		return err
	}
	if err := writeFullBox(w, dref.Context, mp4.BoxTypeUrl(), &mp4.Url{}); err != nil {
		return err
	}
	if err := endBoxes(w, 2); err != nil { // dref, dinf
		return err
	}
	return nil
}

func videoColorSrc(spec FixtureSpec) byte {
	if spec.Video.BitstreamColor != nil {
		return 2 // bitstream
	}
	return 0
}

func colorPrimaries(spec FixtureSpec) uint16 {
	if spec.Video.BitstreamColor != nil {
		return spec.Video.BitstreamColor.Primaries
	}
	if spec.Video.Color != nil {
		return spec.Video.Color.Primaries
	}
	return 0
}
