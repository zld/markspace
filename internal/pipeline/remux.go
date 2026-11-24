package pipeline

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/abema/go-mp4"

	"mediad/internal/adapter"
	"mediad/internal/media"
)

// OutputTimescale is the timescale of remuxed deliveries (microseconds).
const OutputTimescale = 1_000_000

// WriteTrack describes one track to serialize into an MP4 container. The
// same writer backs fixture generation and the lossless remux, so both
// paths produce byte-compatible layouts.
type WriteTrack struct {
	TrackID     int
	Type        media.TrackType
	Language    string
	CodecParams []byte // raw stsd payload, carried verbatim
	Timescale   uint32
	Samples     [][]byte
	Deltas      []uint32     // per-sample duration, ticks
	Keyframes   []int        // sample indices; nil → no stss written
	Edits       []media.Edit // optional elst; durations in track timescale
}

// WriteMP4 serializes tracks as ftyp+moov+mdat. moov is placed before mdat
// so a truncated tail damages sample data — recoverable via gaps — rather
// than the track tables, which would be unrecoverable.
func WriteMP4(w io.Writer, tracks []WriteTrack) error {
	ftyp := marshalBox(&mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 1,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
		},
	})
	moov := buildMoov(tracks, nil)

	// Chunk offsets: one chunk per track, samples concatenated in mdat.
	mdatStart := int64(len(ftyp) + len(moov) + 8) // 8 = mdat header
	offsets := make([]uint32, len(tracks))
	cur := mdatStart
	for i, t := range tracks {
		offsets[i] = uint32(cur)
		for _, s := range t.Samples {
			cur += int64(len(s))
		}
	}
	moov = buildMoov(tracks, offsets)

	if _, err := w.Write(ftyp); err != nil {
		return err
	}
	if _, err := w.Write(moov); err != nil {
		return err
	}
	head := make([]byte, 8)
	binary.BigEndian.PutUint32(head[0:4], uint32(8+cur-mdatStart))
	copy(head[4:8], "mdat")
	if _, err := w.Write(head); err != nil {
		return err
	}
	for _, t := range tracks {
		for _, s := range t.Samples {
			if _, err := w.Write(s); err != nil {
				return err
			}
		}
	}
	return nil
}

// marshalBox serializes a structured box via go-mp4 and wraps the payload
// in its size+type header (mp4.Marshal emits the payload only).
func marshalBox(b mp4.IImmutableBox) []byte {
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, b, mp4.Context{}); err != nil {
		panic(err) // static structs: marshal only fails on writer errors
	}
	typ := b.GetType()
	return containerBox(string(typ[:]), buf.Bytes())
}

func containerBox(typ string, children ...[]byte) []byte {
	size := 8
	for _, c := range children {
		size += len(c)
	}
	out := make([]byte, 8, size)
	binary.BigEndian.PutUint32(out[0:4], uint32(size))
	copy(out[4:8], typ)
	for _, c := range children {
		out = append(out, c...)
	}
	return out
}

func buildMoov(tracks []WriteTrack, chunkOffsets []uint32) []byte {
	var movieDur uint32
	var trakBytes [][]byte
	for i, t := range tracks {
		var dur uint64
		for _, d := range t.Deltas {
			dur += uint64(d)
		}
		if uint32(dur) > movieDur {
			movieDur = uint32(dur)
		}
		var chunk uint32
		if chunkOffsets != nil {
			chunk = chunkOffsets[i]
		}
		trakBytes = append(trakBytes, buildTrak(t, chunk, uint32(dur)))
	}
	mvhd := marshalBox(&mp4.Mvhd{
		Timescale:   1000,
		DurationV0:  movieDur / 1000, // coarse; readers use stts
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix,
		NextTrackID: uint32(len(tracks) + 1),
	})
	return containerBox("moov", append([][]byte{mvhd}, trakBytes...)...)
}

var identityMatrix = [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}

func buildTrak(t WriteTrack, chunkOffset uint32, durTicks uint32) []byte {
	lang := encodeLanguage("und")
	if len(t.Language) == 3 {
		lang = encodeLanguage(t.Language)
	}
	tkhd := marshalBox(&mp4.Tkhd{
		TrackID:    uint32(t.TrackID),
		DurationV0: durTicks,
		Matrix:     identityMatrix,
	})
	mdhd := marshalBox(&mp4.Mdhd{
		Timescale:  t.Timescale,
		DurationV0: durTicks,
		Language:   lang,
	})
	handler := "vide"
	if t.Type == media.TrackAudio {
		handler = "soun"
	}
	hdlr := marshalBox(&mp4.Hdlr{HandlerType: [4]byte{handler[0], handler[1], handler[2], handler[3]}, Name: handler})

	// Media header: vmhd for video, smhd for audio (fixed tiny payloads).
	var mediaHead []byte
	if t.Type == media.TrackVideo {
		mediaHead = fullBox("vmhd", 0, 1, make([]byte, 8))
	} else {
		mediaHead = fullBox("smhd", 0, 0, make([]byte, 4))
	}
	dref := fullBox("dref", 0, 0, append([]byte{0, 0, 0, 1}, fullBox("url ", 0, 1, nil)...))
	dinf := containerBox("dinf", dref)

	stsd := containerBox("stsd", t.CodecParams)
	runs := sttsRuns(t.Deltas)
	stts := marshalBox(&mp4.Stts{EntryCount: uint32(len(runs)), Entries: runs})
	stsc := marshalBox(&mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{
		{FirstChunk: 1, SamplesPerChunk: uint32(len(t.Samples)), SampleDescriptionIndex: 1},
	}})
	stsz := marshalBox(&mp4.Stsz{EntrySize: sampleSizes(t.Samples), SampleCount: uint32(len(t.Samples))})
	stco := marshalBox(&mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{chunkOffset}})
	stblChildren := [][]byte{stsd, stts, stsc, stsz, stco}
	if t.Keyframes != nil {
		nums := make([]uint32, len(t.Keyframes))
		for i, k := range t.Keyframes {
			nums[i] = uint32(k) + 1
		}
		stblChildren = append(stblChildren, marshalBox(&mp4.Stss{EntryCount: uint32(len(nums)), SampleNumber: nums}))
	}
	stbl := containerBox("stbl", stblChildren...)
	minf := containerBox("minf", mediaHead, dinf, stbl)

	mdiaChildren := [][]byte{mdhd, hdlr, minf}
	mdia := containerBox("mdia", mdiaChildren...)

	trakChildren := [][]byte{tkhd}
	if len(t.Edits) > 0 {
		trakChildren = append(trakChildren, containerBox("edts", buildElst(t.Edits, t.Timescale)))
	}
	return containerBox("trak", append(trakChildren, mdia)...)
}

func buildElst(edits []media.Edit, timescale uint32) []byte {
	elst := &mp4.Elst{}
	for _, e := range edits {
		entry := mp4.ElstEntry{
			SegmentDurationV0: uint32(media.NanosToTicks(e.Duration, timescale)),
			MediaRateInteger:  1,
		}
		if e.MediaTime < 0 {
			entry.MediaTimeV0 = -1
		} else {
			entry.MediaTimeV0 = int32(media.NanosToTicks(e.MediaTime, timescale))
		}
		elst.Entries = append(elst.Entries, entry)
	}
	elst.EntryCount = uint32(len(elst.Entries))
	return marshalBox(elst)
}

// encodeLanguage packs an ISO-639-2 code into the 5-bit-per-char form
// go-mp4 stores in mdhd (it does not apply the 0x60 offset itself).
func encodeLanguage(s string) [3]byte {
	var out [3]byte
	for i := 0; i < 3 && i < len(s); i++ {
		out[i] = s[i] - 0x60
	}
	return out
}

func fullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	head := make([]byte, 4)
	head[0] = version
	head[1] = byte(flags >> 16)
	head[2] = byte(flags >> 8)
	head[3] = byte(flags)
	return containerBox(typ, append(head, payload...))
}

func sttsRuns(deltas []uint32) []mp4.SttsEntry {
	var out []mp4.SttsEntry
	for _, d := range deltas {
		if n := len(out); n > 0 && out[n-1].SampleDelta == d {
			out[n-1].SampleCount++
		} else {
			out = append(out, mp4.SttsEntry{SampleCount: 1, SampleDelta: d})
		}
	}
	return out
}

func sampleSizes(samples [][]byte) []uint32 {
	out := make([]uint32, len(samples))
	for i, s := range samples {
		out[i] = uint32(len(s))
	}
	return out
}

// RemuxTrack binds a mapped track to its computed output timeline.
type RemuxTrack struct {
	Mapped   adapter.MappedTrack
	OutTimes []int64 // per kept event, ns on the output timeline
	Keep     []bool  // per event
}

// Remux losslessly rewrites mapped tracks into one MP4: compressed payload
// bytes and codec parameters are carried verbatim; only the container
// timing tables are rebuilt from the normalized output timeline. Damaged
// samples are omitted — they are reported as manifest gaps instead.
func Remux(w io.Writer, tracks []RemuxTrack) error {
	var wts []WriteTrack
	for _, rt := range tracks {
		wt := WriteTrack{
			TrackID:     rt.Mapped.OutputID,
			Type:        rt.Mapped.Track.Type,
			Language:    rt.Mapped.Track.Language,
			CodecParams: rt.Mapped.Track.CodecParams,
			Timescale:   OutputTimescale,
		}
		var kept []int
		for i, ev := range rt.Mapped.Track.Events {
			if i < len(rt.Keep) && rt.Keep[i] && !ev.Damaged {
				kept = append(kept, i)
			}
		}
		for j, ei := range kept {
			ev := rt.Mapped.Track.Events[ei]
			wt.Samples = append(wt.Samples, ev.Payload)
			// stts has no initial-CTS field: sample j's delta is the
			// distance to sample j+1, so CTS_j accumulates to out_j.
			delta := uint32(1)
			if j+1 < len(kept) {
				d := media.NanosToTicks(rt.OutTimes[kept[j+1]]-rt.OutTimes[ei], OutputTimescale)
				if d < 1 {
					d = 1
				}
				delta = uint32(d)
			} else if j > 0 {
				delta = wt.Deltas[j-1] // last sample repeats the previous duration
			}
			wt.Deltas = append(wt.Deltas, delta)
			if ev.Keyframe || (!rt.Mapped.Track.HasKeyframeTable && payloadKeyframe(ev.Payload)) {
				wt.Keyframes = append(wt.Keyframes, j)
			}
		}
		wts = append(wts, wt)
	}
	return WriteMP4(w, wts)
}
