package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/abema/go-mp4"

	"regress/internal/model"
)

// writeMoovEmsg emits one real ISO-BMFF emsg box per non-dropped emsg plan
// item at moov level, so the go-mp4 parser has genuine timed-event boxes to
// read rather than only the custom smpc catalog.
func writeMoovEmsg(w *mp4.Writer, plan []model.PlanItem) error {
	for _, it := range plan {
		if it.Track != MetaEmsg || it.Drop {
			continue
		}
		ticks := uint64(int64(it.ExpectedTick) + int64(it.DelayTicks))
		delta := uint32(ticks * uint64(VideoHz) / 90000)
		msg := []byte(fmt.Sprintf("%s|%d", it.Key, it.Seq))
		emsg := &mp4.Emsg{
			Timescale:             uint32(VideoHz),
			PresentationTimeDelta: delta,
			EventDuration:         uint32(int64(VideoFrameTick) * VideoHz / 90000),
			Id:                    it.Seq,
			MessageData:           msg,
		}
		if err := startBox(w, mp4.BoxTypeEmsg()); err != nil {
			return err
		}
		if err := marshalBox(w, emsg); err != nil {
			return err
		}
		if err := endBox(w); err != nil {
			return err
		}
		// The duplicated item emits a second identical box.
		if it.Duplicate {
			if err := startBox(w, mp4.BoxTypeEmsg()); err != nil {
				return err
			}
			if err := marshalBox(w, emsg); err != nil {
				return err
			}
			if err := endBox(w); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeMdat packs every rendered video frame and audio sample into one mdat
// box in track order. Offsets are derived from catalog sequence numbers, so
// no separate chunk-offset table is needed for this self-describing fixture.
func writeMdat(w *mp4.Writer, s Spec, plan []model.PlanItem) error {
	if err := startBox(w, mp4.BoxTypeMdat()); err != nil {
		return err
	}
	var buf bytes.Buffer
	u16 := func(v uint16) {
		var tmp [2]byte
		binary.BigEndian.PutUint16(tmp[:], v)
		buf.Write(tmp[:])
	}
	for seq := uint32(0); seq < uint32(s.VideoFrames); seq++ {
		frame := FramePixels(s.Seed, seq)
		u16(1) // rendition index 1 = high
		buf.Write(frame)
	}
	for seq := uint32(0); seq < uint32(s.VideoFrames); seq++ {
		frame := FramePixels(s.Seed^0xffff, seq)
		u16(2) // rendition index 2 = low
		buf.Write(frame)
	}
	for seq := uint32(0); seq < uint32(s.VideoFrames); seq++ {
		pcm := AudioSamples(s.Seed, seq)
		u16(3) // audio stream
		for _, v := range pcm {
			var tmp [2]byte
			binary.LittleEndian.PutUint16(tmp[:], uint16(v))
			buf.Write(tmp[:])
		}
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return err
	}
	return endBox(w)
}
