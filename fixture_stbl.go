package mediacore

import (
	mp4 "github.com/abema/go-mp4"
)

func writeStbl(w *mp4.Writer, ctx mp4.Context, tracks []muxTrack, chunks []muxChunk, ti int) error {
	tr := tracks[ti]
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if err := writeSampleEntry(w, ctx, tr); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if err := writeStts(w, ctx, tr); err != nil {
		return err
	}
	if err := writeCtts(w, ctx, tr); err != nil {
		return err
	}
	if err := writeStsc(w, ctx, tr, chunks); err != nil {
		return err
	}
	if err := writeStsz(w, ctx, tr); err != nil {
		return err
	}
	if err := writeStco(w, ctx, chunks); err != nil {
		return err
	}
	if err := writeStss(w, ctx, tr); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}

func writeStts(w *mp4.Writer, ctx mp4.Context, tr muxTrack) error {
	var entries []mp4.SttsEntry
	for _, s := range tr.Samples {
		n := len(entries)
		if n > 0 && entries[n-1].SampleDelta == s.Dur {
			entries[n-1].SampleCount++
			continue
		}
		entries = append(entries, mp4.SttsEntry{SampleCount: 1, SampleDelta: s.Dur})
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{EntryCount: uint32(len(entries)), Entries: entries}, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}

func writeCtts(w *mp4.Writer, ctx mp4.Context, tr muxTrack) error {
	needed := false
	var entries []mp4.CttsEntry
	for _, s := range tr.Samples {
		off := s.CTS - s.DTS
		if off != 0 {
			needed = true
		}
		value := uint32(off)
		n := len(entries)
		if n > 0 && entries[n-1].SampleOffsetV0 == value {
			entries[n-1].SampleCount++
			continue
		}
		entries = append(entries, mp4.CttsEntry{SampleCount: 1, SampleOffsetV0: value})
	}
	if !needed {
		return nil
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeCtts()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Ctts{EntryCount: uint32(len(entries)), Entries: entries}, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}

func writeStsc(w *mp4.Writer, ctx mp4.Context, tr muxTrack, chunks []muxChunk) error {
	var entries []mp4.StscEntry
	chunkNumber := uint32(0)
	for _, ch := range chunks {
		chunkNumber++
		n := len(entries)
		if n > 0 && entries[n-1].SamplesPerChunk == uint32(ch.Count) {
			continue
		}
		entries = append(entries, mp4.StscEntry{FirstChunk: chunkNumber, SamplesPerChunk: uint32(ch.Count), SampleDescriptionIndex: 1})
	}
	if len(entries) == 0 {
		return fail(KindInternal, "mux", "track %d has no chunks", tr.ID)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Stsc{EntryCount: uint32(len(entries)), Entries: entries}, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}

func writeStsz(w *mp4.Writer, ctx mp4.Context, tr muxTrack) error {
	sizes := make([]uint32, len(tr.Samples))
	var uniform uint32
	same := true
	for i, s := range tr.Samples {
		sizes[i] = uint32(s.Size)
		if i == 0 {
			uniform = sizes[i]
		} else if sizes[i] != uniform {
			same = false
		}
	}
	stsz := &mp4.Stsz{SampleCount: uint32(len(sizes))}
	if same {
		stsz.SampleSize = uniform
	} else {
		stsz.EntrySize = sizes
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := mp4.Marshal(w, stsz, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}

func writeStco(w *mp4.Writer, ctx mp4.Context, chunks []muxChunk) error {
	offsets := make([]uint32, len(chunks))
	for i, ch := range chunks {
		offsets[i] = uint32(ch.Start)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Stco{EntryCount: uint32(len(offsets)), ChunkOffset: offsets}, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}

func writeStss(w *mp4.Writer, ctx mp4.Context, tr muxTrack) error {
	all := true
	var nums []uint32
	for i, s := range tr.Samples {
		if !s.Sync {
			all = false
			continue
		}
		nums = append(nums, uint32(i+1))
	}
	if all {
		return nil // absence of stss means every sample is sync
	}
	if len(nums) == 0 {
		return fail(KindInternal, "mux", "track %d has no sync samples", tr.ID)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Stss{EntryCount: uint32(len(nums)), SampleNumber: nums}, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}
