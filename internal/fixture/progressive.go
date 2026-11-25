package fixture

import (
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"
)

// WriteProgressiveMP4 writes a non-fragmented file using stts/stsz/stco tables.
// Each sample is placed in its own chunk, making offsets deterministic.
func WriteProgressiveMP4(path string, timescale uint32, samples []SampleSpec) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var duration uint64
	var payloadSize int64
	sttsEntries := make([]mp4.SttsEntry, 0, len(samples))
	sizes := make([]uint32, 0, len(samples))
	offsets := make([]uint32, len(samples))
	for _, s := range samples {
		duration += uint64(s.Duration)
		payloadSize += int64(s.Size)
		sttsEntries = append(sttsEntries, mp4.SttsEntry{SampleCount: 1, SampleDelta: s.Duration})
		sizes = append(sizes, s.Size)
	}

	mdhdBox, err := marshalBox(&mp4.Mdhd{
		FullBox:    mp4.FullBox{Version: 0},
		Timescale:  timescale,
		DurationV0: uint32(duration),
	})
	if err != nil {
		return err
	}
	sttsBox, err := marshalBox(&mp4.Stts{
		FullBox:    mp4.FullBox{Version: 0},
		EntryCount: uint32(len(sttsEntries)),
		Entries:    sttsEntries,
	})
	if err != nil {
		return err
	}
	stszBox, err := marshalBox(&mp4.Stsz{
		FullBox:     mp4.FullBox{Version: 0},
		SampleSize:  0,
		SampleCount: uint32(len(samples)),
		EntrySize:   sizes,
	})
	if err != nil {
		return err
	}
	stcoBox, err := marshalBox(&mp4.Stco{
		FullBox:     mp4.FullBox{Version: 0},
		EntryCount:  uint32(len(offsets)),
		ChunkOffset: offsets,
	})
	if err != nil {
		return err
	}
	buildMoov := func(stco []byte) []byte {
		stbl := box("stbl", sttsBox, stszBox, stco)
		minf := box("minf", stbl)
		mdia := box("mdia", mdhdBox, minf)
		trak := box("trak", mdia)
		return box("moov", trak)
	}
	ftyp := box("ftyp", append(append([]byte("iso5"), u32(0x200)...), []byte("iso5mp42")...))
	moovPlaceholder := buildMoov(stcoBox)
	dataStart := uint32(len(ftyp) + len(moovPlaceholder) + 8)
	cursor := dataStart
	for i, s := range samples {
		offsets[i] = cursor
		cursor += s.Size
	}
	stcoBox, err = marshalBox(&mp4.Stco{
		FullBox:     mp4.FullBox{Version: 0},
		EntryCount:  uint32(len(offsets)),
		ChunkOffset: offsets,
	})
	if err != nil {
		return err
	}
	moov := buildMoov(stcoBox)
	if len(moov) != len(moovPlaceholder) {
		return &buildError{"progressive moov size changed after offset fill"}
	}
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}
	return os.WriteFile(path, append(append(ftyp, moov...), box("mdat", payload)...), 0o644)
}
