package fixture

import (
	"encoding/binary"
	"fmt"

	"github.com/abema/go-mp4"
)

// buildMP4 writes the fragmented container. It returns the bytes and the
// absolute offset of every moof box.
func buildMP4(p plan, corrupt bool) ([]byte, []int64, error) {
	var out memSeeker
	w := mp4.NewWriter(&out)
	if err := writeFtyp(w); err != nil {
		return nil, nil, err
	}
	if err := writeMoov(w, 0); err != nil {
		return nil, nil, err
	}

	var moofOffsets []int64
	var dialogueTicks, effectsTicks, subtitleTicks uint64
	for f := 0; f < len(p.dialogue); f++ {
		moofStart := int64(len(out.buf))
		frag := fragmentPlan{
			seq:          uint32(f + 1),
			dialogue:     p.dialogue[f],
			effects:      p.effects[f],
			subtitle:     p.subtitle[f],
			dialogueBase: dialogueTicks,
			effectsBase:  effectsTicks,
			subtitleBase: subtitleTicks,
		}
		for _, s := range p.dialogue[f] {
			dialogueTicks += s.samples
		}
		for _, s := range p.effects[f] {
			effectsTicks += s.samples
		}
		var subtitleDuration uint64
		for _, s := range p.dialogue[f] {
			subtitleDuration += s.samples
		}
		subtitleTicks += subtitleDuration

		moof, err := buildMoof(frag, moofStart)
		if err != nil {
			return nil, nil, err
		}
		moofOffsets = append(moofOffsets, moofStart)
		if _, err := out.Write(moof); err != nil {
			return nil, nil, err
		}
		payload := fragmentPayload(frag)
		if err := withBox(w, mp4.BoxTypeMdat(), func() error {
			_, werr := w.Write(payload)
			return werr
		}); err != nil {
			return nil, nil, err
		}
	}
	data := out.buf
	if corrupt && len(moofOffsets) > 1 {
		off := moofOffsets[1]
		binary.BigEndian.PutUint32(data[off:off+4], 0xFFFFF000)
	}
	return data, moofOffsets, nil
}

type fragmentPlan struct {
	seq          uint32
	dialogue     []audioSample
	effects      []audioSample
	subtitle     [][]byte
	dialogueBase uint64
	effectsBase  uint64
	subtitleBase uint64
}

func fragmentPayload(f fragmentPlan) []byte {
	var buf []byte
	for _, s := range f.dialogue {
		buf = append(buf, s.payload...)
	}
	for _, s := range f.effects {
		buf = append(buf, s.payload...)
	}
	for _, t := range f.subtitle {
		buf = append(buf, t...)
	}
	return buf
}

// buildMoof renders a moof twice: once to learn its length, then again with
// trun data offsets that point at the mdat payload that follows it.
func buildMoof(f fragmentPlan, moofStart int64) ([]byte, error) {
	subtitleBytes := 0
	for _, t := range f.subtitle {
		subtitleBytes += len(t)
	}
	dialogueBytes := 0
	for _, s := range f.dialogue {
		dialogueBytes += len(s.payload)
	}
	effectsBytes := 0
	for _, s := range f.effects {
		effectsBytes += len(s.payload)
	}
	payloadOffsets := [3]int{0, dialogueBytes, dialogueBytes + effectsBytes}

	measure, err := renderMoof(f, moofStart, [3]int{0, 0, 0})
	if err != nil {
		return nil, err
	}
	moofLen := len(measure)
	// Each fragment is followed immediately by its mdat (8 byte header).
	base := moofLen + 8
	offsets := [3]int{
		base + payloadOffsets[0],
		base + payloadOffsets[1],
		base + payloadOffsets[2],
	}
	rendered, err := renderMoof(f, moofStart, offsets)
	if err != nil {
		return nil, err
	}
	if len(rendered) != moofLen {
		return nil, fmt.Errorf("fixture: moof length changed between passes (%d vs %d)", moofLen, len(rendered))
	}
	return rendered, nil
}

func renderMoof(f fragmentPlan, moofStart int64, dataOffsets [3]int) ([]byte, error) {
	var ms memSeeker
	w := mp4.NewWriter(&ms)
	err := withBox(w, mp4.BoxTypeMoof(), func() error {
		if err := withBox(w, mp4.BoxTypeMfhd(), func() error {
			return marshalBox(w, &mp4.Mfhd{FullBox: mp4.FullBox{Version: 0}, SequenceNumber: f.seq})
		}); err != nil {
			return err
		}
		trafs := []struct {
			trackID uint32
			samples []audioSample
			base    uint64
			dataOff int
		}{
			{1, f.dialogue, f.dialogueBase, dataOffsets[0]},
			{2, f.effects, f.effectsBase, dataOffsets[1]},
		}
		for _, tr := range trafs {
			if err := writeTraf(w, tr.trackID, tr.samples, tr.base, moofStart, tr.dataOff); err != nil {
				return err
			}
		}
		return writeSubtitleTraf(w, f, moofStart, dataOffsets[2])
	})
	if err != nil {
		return nil, err
	}
	return ms.buf, nil
}

func writeTraf(w *mp4.Writer, trackID uint32, samples []audioSample, base uint64, moofStart int64, dataOff int) error {
	return withBox(w, mp4.BoxTypeTraf(), func() error {
		if err := withBox(w, mp4.BoxTypeTfhd(), func() error {
			return marshalBox(w, &mp4.Tfhd{
				FullBox:        mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 0x01}},
				TrackID:        trackID,
				BaseDataOffset: uint64(moofStart),
			})
		}); err != nil {
			return err
		}
		if err := withBox(w, mp4.BoxTypeTfdt(), func() error {
			return marshalBox(w, &mp4.Tfdt{FullBox: mp4.FullBox{Version: 0}, BaseMediaDecodeTimeV0: uint32(base)})
		}); err != nil {
			return err
		}
		return withBox(w, mp4.BoxTypeTrun(), func() error {
			trun := &mp4.Trun{
				FullBox:     mp4.FullBox{Version: 0, Flags: [3]byte{0, 0x03, 0x01}},
				SampleCount: uint32(len(samples)),
				DataOffset:  int32(dataOff),
				Entries:     make([]mp4.TrunEntry, 0, len(samples)),
			}
			for _, s := range samples {
				trun.Entries = append(trun.Entries, mp4.TrunEntry{
					SampleDuration: uint32(s.samples),
					SampleSize:     uint32(len(s.payload)),
				})
			}
			return marshalBox(w, trun)
		})
	})
}

func writeSubtitleTraf(w *mp4.Writer, f fragmentPlan, moofStart int64, dataOff int) error {
	var duration uint64
	for _, s := range f.dialogue {
		duration += s.samples
	}
	return withBox(w, mp4.BoxTypeTraf(), func() error {
		if err := withBox(w, mp4.BoxTypeTfhd(), func() error {
			return marshalBox(w, &mp4.Tfhd{
				FullBox:        mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 0x01}},
				TrackID:        3,
				BaseDataOffset: uint64(moofStart),
			})
		}); err != nil {
			return err
		}
		if err := withBox(w, mp4.BoxTypeTfdt(), func() error {
			return marshalBox(w, &mp4.Tfdt{FullBox: mp4.FullBox{Version: 0}, BaseMediaDecodeTimeV0: uint32(f.subtitleBase)})
		}); err != nil {
			return err
		}
		return withBox(w, mp4.BoxTypeTrun(), func() error {
			trun := &mp4.Trun{
				FullBox:     mp4.FullBox{Version: 0, Flags: [3]byte{0, 0x03, 0x01}},
				SampleCount: uint32(len(f.subtitle)),
				DataOffset:  int32(dataOff),
				Entries:     make([]mp4.TrunEntry, 0, len(f.subtitle)),
			}
			for _, t := range f.subtitle {
				trun.Entries = append(trun.Entries, mp4.TrunEntry{
					SampleDuration: uint32(duration),
					SampleSize:     uint32(len(t)),
				})
			}
			return marshalBox(w, trun)
		})
	})
}
