package demux

import (
	"fmt"
	"io"
	"os"

	gomp4 "github.com/abema/go-mp4"

	"medpipe/internal/fault"
)

// MaxMP4InputBytes bounds MP4 inputs and logs.
const MaxMP4InputBytes = 128 << 20

// ReadMP4File opens and demultiplexes track/sample metadata from an MP4 file.
func ReadMP4File(path string) (*Media, *fault.Error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.Wrap(fault.ErrOutputUnreadable, "demux.mp4.open", 0, "open MP4", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fault.Wrap(fault.ErrOutputUnreadable, "demux.mp4.stat", 0, "stat MP4", err)
	}
	if st.Size() > MaxMP4InputBytes {
		return nil, fault.New(fault.ErrInputTooLarge, "demux.mp4", 0, "MP4 input exceeds size limit")
	}
	return ReadMP4(f)
}

// ReadMP4 reads supported tracks and compressed samples using go-mp4 box
// definitions for sample tables.
func ReadMP4(r io.ReadSeeker) (*Media, *fault.Error) {
	trakPath := gomp4.BoxPath{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak()}
	traks, err := gomp4.ExtractBox(r, nil, trakPath)
	if err != nil {
		return nil, fault.Wrap(fault.ErrOutputUnreadable, "demux.mp4.boxes", 0, "extract traks", err)
	}
	if len(traks) == 0 {
		return nil, fault.New(fault.ErrOutputUnreadable, "demux.mp4.trak", 0, "no trak boxes")
	}

	media := &Media{Format: "mp4"}
	for i, trak := range traks {
		t := &Track{ID: uint32(i + 1), Timescale: 1000}
		if tkhd := firstPayload[*gomp4.Tkhd](r, gomp4.BoxPath{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeTkhd()}, trak); tkhd != nil {
			t.ID = tkhd.TrackID
			t.Width = uint16(tkhd.Width >> 16)
			t.Height = uint16(tkhd.Height >> 16)
		}
		if mdhd := firstPayload[*gomp4.Mdhd](r, gomp4.BoxPath{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMdhd()}, trak); mdhd != nil {
			t.Timescale = mdhd.Timescale
		}
		if hdlr := firstPayload[*gomp4.Hdlr](r, gomp4.BoxPath{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeHdlr()}, trak); hdlr != nil {
			switch hdlr.HandlerType {
			case [4]byte{'v', 'i', 'd', 'e'}:
				t.Kind = "video"
			case [4]byte{'s', 'o', 'u', 'n'}:
				t.Kind = "audio"
			}
		}
		codec, kind, fe := readCodec(r, trak)
		if fe != nil {
			return nil, fe
		}
		t.Codec = codec
		if t.Kind == "" {
			t.Kind = kind
		}
		if t.Kind == "audio" {
			t.SampleRate = 48000
			t.Channels = 2
		}

		stts := firstPayload[*gomp4.Stts](r, gomp4.BoxPath{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStts()}, trak)
		stsz := firstPayload[*gomp4.Stsz](r, gomp4.BoxPath{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStsz()}, trak)
		stco := firstPayload[*gomp4.Stco](r, gomp4.BoxPath{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStco()}, trak)
		stsc := firstPayload[*gomp4.Stsc](r, gomp4.BoxPath{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStsc()}, trak)
		if stts == nil || stsz == nil || stco == nil || stsc == nil {
			return nil, fault.New(fault.ErrOutputUnreadable, "demux.mp4.stbl", int64(trak.Offset), "missing required sample table")
		}
		sizes := expandSizes(stsz)
		deltas := expandDeltas(stts, len(sizes))
		offsets := expandChunks(stsc, stco, sizes)
		if len(offsets) != len(sizes) {
			return nil, fault.New(fault.ErrOutputUnreadable, "demux.mp4.stsc", int64(trak.Offset),
				fmt.Sprintf("sample offsets %d != sizes %d", len(offsets), len(sizes)))
		}
		var ticks uint64
		for j, size := range sizes {
			off := offsets[j]
			data := make([]byte, size)
			if _, err := r.Seek(int64(off), io.SeekStart); err != nil {
				return nil, fault.Wrap(fault.ErrOutputUnreadable, "demux.mp4.seek", int64(off), "seek sample", err)
			}
			if _, err := io.ReadFull(r, data); err != nil {
				return nil, fault.Wrap(fault.ErrCorruptSample, "demux.mp4.sample", int64(off), "read compressed sample", err)
			}
			ms := int64(ticks * 1000 / uint64(t.Timescale))
			d := int64(0)
			if j < len(deltas) {
				d = int64(uint64(deltas[j]) * 1000 / uint64(t.Timescale))
				ticks += uint64(deltas[j])
			}
			t.Samples = append(t.Samples, Sample{Index: j, PTSMS: ms, DurationMS: d, Offset: int64(off), Data: data})
		}
		media.Tracks = append(media.Tracks, t)
	}
	return media, nil
}

func firstPayload[T any](r io.ReadSeeker, path gomp4.BoxPath, trak *gomp4.BoxInfo) T {
	var zero T
	boxes, err := gomp4.ExtractBoxesWithPayload(r, nil, []gomp4.BoxPath{path})
	if err != nil {
		return zero
	}
	for _, b := range boxes {
		if trak != nil && (b.Info.Offset < trak.Offset || b.Info.Offset >= trak.Offset+trak.Size) {
			continue
		}
		if v, ok := b.Payload.(T); ok {
			return v
		}
	}
	return zero
}

func readCodec(r io.ReadSeeker, trak *gomp4.BoxInfo) (codec, kind string, err *fault.Error) {
	codecTypes := []gomp4.BoxType{gomp4.StrToBoxType("avc1"), gomp4.StrToBoxType("hvc1"), gomp4.StrToBoxType("mp4a")}
	var paths []gomp4.BoxPath
	for _, ct := range codecTypes {
		paths = append(paths, gomp4.BoxPath{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStsd(), ct})
	}
	boxes, gerr := gomp4.ExtractBoxes(r, nil, paths)
	if gerr != nil {
		return "", "", fault.Wrap(fault.ErrOutputUnreadable, "demux.mp4.stsd", 0, "extract codec sample entry", gerr)
	}
	for _, b := range boxes {
		if b.Offset < trak.Offset || b.Offset >= trak.Offset+trak.Size {
			continue
		}
		switch b.Type {
		case gomp4.StrToBoxType("avc1"):
			return "avc1", "video", nil
		case gomp4.StrToBoxType("hvc1"):
			return "hvc1", "video", nil
		case gomp4.StrToBoxType("mp4a"):
			return "mp4a", "audio", nil
		}
	}
	return "", "", fault.New(fault.ErrUnknownCodec, "demux.mp4.stsd", int64(trak.Offset), "no supported avc1/hvc1/mp4a sample entry")
}

func expandSizes(stsz *gomp4.Stsz) []uint32 {
	n := int(stsz.SampleCount)
	out := make([]uint32, 0, n)
	if stsz.SampleSize != 0 {
		for i := 0; i < n; i++ {
			out = append(out, stsz.SampleSize)
		}
		return out
	}
	return append(out, stsz.EntrySize...)
}

func expandDeltas(stts *gomp4.Stts, n int) []uint32 {
	out := make([]uint32, 0, n)
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount && len(out) < n; i++ {
			out = append(out, e.SampleDelta)
		}
	}
	for len(out) < n {
		out = append(out, 0)
	}
	return out
}

func expandChunks(stsc *gomp4.Stsc, stco *gomp4.Stco, sizes []uint32) []uint32 {
	var out []uint32
	sampleIdx := 0
	for chunkIdx := 1; chunkIdx <= len(stco.ChunkOffset); chunkIdx++ {
		var entry gomp4.StscEntry
		for _, e := range stsc.Entries {
			if int(e.FirstChunk) <= chunkIdx {
				entry = e
			}
		}
		off := stco.ChunkOffset[chunkIdx-1]
		per := int(entry.SamplesPerChunk)
		if per == 0 {
			per = 1
		}
		for j := 0; j < per && sampleIdx < len(sizes); j++ {
			out = append(out, off)
			off += sizes[sampleIdx]
			sampleIdx++
		}
	}
	return out
}
