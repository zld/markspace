package demux

import (
	"github.com/abema/go-mp4"
)

var identityMatrix = [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

func writeMoov(w *mp4.Writer, spec FixtureSpec, videoBytes, audioBytes uint64) error {
	moov, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()})
	if err != nil {
		return err
	}
	movieDur := uint64(spec.Video.FrameCount) * uint64(spec.Video.FrameDelta)
	mvhd, err := startChild(w, moov.Context, mp4.BoxTypeMvhd())
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Mvhd{
		Timescale:   spec.Video.Timescale,
		DurationV0:  uint32(movieDur),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix,
		NextTrackID: 3,
	}, mvhd.Context); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if err := writeVideoTrak(w, moov.Context, spec, videoBytes); err != nil {
		return err
	}
	if err := writeAudioTrak(w, moov.Context, spec, audioBytes); err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func startChild(w *mp4.Writer, ctx mp4.Context, t mp4.BoxType) (*mp4.BoxInfo, error) {
	return w.StartBox(&mp4.BoxInfo{Type: t, Context: ctx})
}

// writeFullBox emits a complete box: header (via StartBox), marshalled
// payload, then size back-patch (via EndBox). Every full/regular box must go
// through here; calling Marshal alone would omit the eight-byte box header.
func writeFullBox(w *mp4.Writer, parent mp4.Context, t mp4.BoxType, box mp4.IImmutableBox) error {
	bi, err := startChild(w, parent, t)
	if err != nil {
		return err
	}
	if err := marshalBox(w, bi, box); err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func marshalBox(w *mp4.Writer, bi *mp4.BoxInfo, box mp4.IImmutableBox) error {
	_, err := mp4.Marshal(w, box, bi.Context)
	return err
}
