package media

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediad/index"
	"mediad/timeline"
)

type stblTables struct {
	stts, stsc, stsz, stco, co64, stsd, stss, ctts []byte
}

// parseSampleTable materialises the sample list from the stbl children.
func parseSampleTable(r *bytes.Reader, path string, stbl boxSpan, t *Track) error {
	var tab stblTables
	kids, err := allChildren(r, stbl)
	if err != nil {
		return locate(path, int64(stbl.offset), err)
	}
	for _, k := range kids {
		payload := make([]byte, k.payloadSize())
		if _, err := r.Seek(int64(k.payloadStart()), io.SeekStart); err != nil {
			return locate(path, int64(k.offset), err)
		}
		if _, err := io.ReadFull(r, payload); err != nil {
			return locate(path, int64(k.offset), fmt.Errorf("read %q payload: %w", string(k.typ[:]), err))
		}
		switch string(k.typ[:]) {
		case "stts":
			tab.stts = payload
		case "stsc":
			tab.stsc = payload
		case "stsz":
			tab.stsz = payload
		case "stco":
			tab.stco = payload
		case "co64":
			tab.co64 = payload
		case "stsd":
			tab.stsd = payload
		case "stss":
			tab.stss = payload
		case "ctts":
			tab.ctts = payload
		}
	}
	if tab.stsz == nil || tab.stts == nil || (tab.stco == nil && tab.co64 == nil) {
		if t.Kind == KindVideo || t.Kind == KindAudio {
			return &timeline.LocatedError{Issue: timeline.Issue{
				Severity: timeline.SeverityError, Code: timeline.CodeUnsupported,
				File: path, TrackID: t.ID, ByteOffset: int64(stbl.offset),
				Message: "track has no complete sample table (stsz/stts/stco)",
			}}
		}
		// A metadata-only track may legitimately be empty.
		return nil
	}

	var stsd mp4.Stsd
	if tab.stsd != nil {
		if _, err := mp4.Unmarshal(bytes.NewReader(tab.stsd), uint64(len(tab.stsd)), &stsd, mp4.Context{}); err != nil {
			return locate(path, int64(stbl.offset), fmt.Errorf("stsd: %w", err))
		}
		if err := parseSampleEntry(r, tab.stsd, &stsd, t); err != nil {
			return locate(path, int64(stbl.offset), err)
		}
	}

	var stts mp4.Stts
	if _, err := mp4.Unmarshal(bytes.NewReader(tab.stts), uint64(len(tab.stts)), &stts, mp4.Context{}); err != nil {
		return locate(path, int64(stbl.offset), fmt.Errorf("stts: %w", err))
	}
	var stsz mp4.Stsz
	if _, err := mp4.Unmarshal(bytes.NewReader(tab.stsz), uint64(len(tab.stsz)), &stsz, mp4.Context{}); err != nil {
		return locate(path, int64(stbl.offset), fmt.Errorf("stsz: %w", err))
	}
	var stsc mp4.Stsc
	if _, err := mp4.Unmarshal(bytes.NewReader(tab.stsc), uint64(len(tab.stsc)), &stsc, mp4.Context{}); err != nil {
		return locate(path, int64(stbl.offset), fmt.Errorf("stsc: %w", err))
	}
	var offsets []uint64
	if tab.co64 != nil {
		var co64 mp4.Co64
		if _, err := mp4.Unmarshal(bytes.NewReader(tab.co64), uint64(len(tab.co64)), &co64, mp4.Context{}); err != nil {
			return locate(path, int64(stbl.offset), fmt.Errorf("co64: %w", err))
		}
		offsets = co64.ChunkOffset
	} else {
		var stco mp4.Stco
		if _, err := mp4.Unmarshal(bytes.NewReader(tab.stco), uint64(len(tab.stco)), &stco, mp4.Context{}); err != nil {
			return locate(path, int64(stbl.offset), fmt.Errorf("stco: %w", err))
		}
		for _, o := range stco.ChunkOffset {
			offsets = append(offsets, uint64(o))
		}
	}
	if offsetsErr := checkChunkOffsets(offsets); offsetsErr != nil {
		return locate(path, int64(stbl.offset), offsetsErr)
	}

	syncSet := map[uint32]bool{}
	syncDefault := t.Kind == KindAudio || t.Kind == KindSubtitle
	if tab.stss != nil {
		var stss mp4.Stss
		if _, err := mp4.Unmarshal(bytes.NewReader(tab.stss), uint64(len(tab.stss)), &stss, mp4.Context{}); err != nil {
			return locate(path, int64(stbl.offset), fmt.Errorf("stss: %w", err))
		}
		for _, n := range stss.SampleNumber {
			syncSet[n] = true
		}
		syncDefault = false
	}

	ctsOffsets, err := parseCtts(tab.ctts) // map sample number -> offset in ticks
	if err != nil {
		return locate(path, int64(stbl.offset), err)
	}

	count := uint64(stsz.SampleCount)
	if count == 0 {
		return nil
	}
	durations, err := expandStts(stts.Entries, count)
	if err != nil {
		return locate(path, int64(stbl.offset), err)
	}
	fileSizes, err := expandStsz(stsz)
	if err != nil {
		return locate(path, int64(stbl.offset), err)
	}
	chunkSamples, err := expandStsc(stsc.Entries, uint32(len(offsets)), uint32(count))
	if err != nil {
		return locate(path, int64(stbl.offset), err)
	}

	samples := make([]index.SampleIndex, 0, count)
	var dts int64
	chunk := -1 // current chunk index; -1 means none selected yet
	var withinChunk uint32
	var chunkCursor int64 // byte offset of the next sample inside the chunk
	for i := uint64(0); i < count; i++ {
		if chunk < 0 || withinChunk >= chunkSamples[chunk] {
			chunk++
			withinChunk = 0
			if chunk >= len(offsets) {
				return locate(path, int64(stbl.offset),
					fmt.Errorf("stsc/stco provide %d chunks but %d samples require more", len(offsets), count))
			}
			chunkCursor = int64(offsets[chunk])
		}
		num := uint32(i + 1)
		s := index.SampleIndex{
			Offset:         chunkCursor,
			Size:           int64(fileSizes[i]),
			DecodeTicks:    dts,
			CompositeTicks: dts + ctsOffsets[num],
			DurationTicks:  int64(durations[i]),
			Sync:           syncSet[num] || syncDefault,
		}
		if s.Size <= 0 {
			return locate(path, int64(offsets[chunk]),
				fmt.Errorf("sample %d has non-positive size %d", num, s.Size))
		}
		samples = append(samples, s)
		dts += int64(durations[i])
		chunkCursor += s.Size
		withinChunk++
	}
	t.Samples = samples
	return nil
}

func checkChunkOffsets(offsets []uint64) error {
	for i, o := range offsets {
		if o == 0 {
			return fmt.Errorf("chunk offset %d is zero (would be a wild pointer)", i)
		}
	}
	return nil
}

func expandStts(entries []mp4.SttsEntry, count uint64) ([]uint32, error) {
	out := make([]uint32, 0, count)
	for _, e := range entries {
		for j := uint32(0); j < e.SampleCount; j++ {
			out = append(out, e.SampleDelta)
			if uint64(len(out)) > count {
				return nil, fmt.Errorf("stts describes more samples than stsz (%d)", count)
			}
		}
	}
	if uint64(len(out)) != count {
		return nil, fmt.Errorf("stts describes %d samples but stsz has %d", len(out), count)
	}
	return out, nil
}

func expandStsz(stsz mp4.Stsz) ([]uint32, error) {
	if stsz.SampleSize != 0 {
		out := make([]uint32, stsz.SampleCount)
		for i := range out {
			out[i] = stsz.SampleSize
		}
		return out, nil
	}
	if uint32(len(stsz.EntrySize)) != stsz.SampleCount {
		return nil, fmt.Errorf("stsz has %d sizes for %d samples", len(stsz.EntrySize), stsz.SampleCount)
	}
	return stsz.EntrySize, nil
}

// expandStsc expands the sample-to-chunk table into samples-per-chunk for every
// chunk index.
func expandStsc(entries []mp4.StscEntry, chunkCount, sampleCount uint32) ([]uint32, error) {
	if len(entries) == 0 {
		return nil, errors.New("empty stsc")
	}
	out := make([]uint32, chunkCount)
	for i, e := range entries {
		first := e.FirstChunk
		if first == 0 || first > chunkCount {
			return nil, fmt.Errorf("stsc entry %d has first_chunk %d outside %d chunks", i, first, chunkCount)
		}
		next := chunkCount + 1
		if i+1 < len(entries) {
			next = entries[i+1].FirstChunk
		}
		if next <= first {
			return nil, fmt.Errorf("stsc entry %d is not increasing", i)
		}
		for c := first; c < next && c <= chunkCount; c++ {
			out[c-1] = e.SamplesPerChunk
		}
	}
	var total uint64
	for _, n := range out {
		total += uint64(n)
	}
	if total < uint64(sampleCount) {
		return nil, fmt.Errorf("stsc covers %d sample slots for %d samples", total, sampleCount)
	}
	return out, nil
}
