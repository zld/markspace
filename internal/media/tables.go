package media

import mp4 "github.com/abema/go-mp4"

func writeSampleTables(w *mp4.Writer, t Track) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 0}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	delta := uint32(1)
	if len(t.Samples) > 0 && t.Samples[0].Delta != 0 {
		delta = t.Samples[0].Delta
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	stts := &mp4.Stts{}
	if len(t.Samples) > 0 {
		stts.EntryCount = 1
		stts.Entries = []mp4.SttsEntry{{SampleCount: uint32(len(t.Samples)), SampleDelta: delta}}
	}
	if _, err := mp4.Marshal(w, stts, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}}}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	sizes := make([]uint32, len(t.Samples))
	var sync []uint32
	for i, s := range t.Samples {
		sizes[i] = uint32(len(s.Data))
		if s.Sync {
			sync = append(sync, uint32(i+1))
		}
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(len(sizes)), EntrySize: sizes}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stco{EntryCount: uint32(len(t.offsets)), ChunkOffset: t.offsets}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	if len(sync) > 0 && len(sync) < len(t.Samples) {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, &mp4.Stss{EntryCount: uint32(len(sync)), SampleNumber: sync}, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	return nil
}
