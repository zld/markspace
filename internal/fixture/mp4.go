// Package fixture generates deterministic sample media — MP4 containers
// and RTP packet dumps — for tests and the demo CLI. All bytes are
// synthesized in code; no external files are referenced.
package fixture

import (
	"bytes"
	"encoding/binary"
	"os"

	mp4 "github.com/abema/go-mp4"
)

// TrackSpec describes one synthetic MP4 track.
type TrackSpec struct {
	Handler   string // 4cc handler: "vide", "soun", anything else = transition
	Timescale uint32
	Deltas    []uint32 // per-sample durations; VFR preserved verbatim
	CTTS      []int32  // per-sample composition offsets; nil = PTS=DTS
	SizeOf    func(i int) uint32
	PayloadOf func(i int) int // bytes actually written; nil = SizeOf
}

// DefaultSize is the deterministic default sample size function.
func DefaultSize(i int) uint32 { return 96 + uint32((i*53)%64) }

func (s *TrackSpec) size(i int) uint32 {
	if s.SizeOf != nil {
		return s.SizeOf(i)
	}
	return DefaultSize(i)
}

func (s *TrackSpec) payload(i int) int {
	if s.PayloadOf != nil {
		return s.PayloadOf(i)
	}
	return int(s.size(i))
}

// writeBox emits one complete box: header (patched by EndBox) + payload.
func writeBox(w *mp4.Writer, box mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: box.GetType()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

// WriteMP4 writes ftyp, mdat, moov (moov last) with major brand "isom".
func WriteMP4(path string, specs []TrackSpec) error {
	return WriteMP4WithBrand(path, [4]byte{'i', 's', 'o', 'm'}, specs)
}

// WriteMP4WithBrand is WriteMP4 with a custom major brand: identical
// samples under a different wrapper model a pure re-mux.
//
// Sample tables declare SizeOf, but only PayloadOf bytes are present in
// mdat, so a shortfall models a truncated sample. Chunk offsets follow
// the declared sizes, exactly like a file whose media data was cut short.
func WriteMP4WithBrand(path string, brand [4]byte, specs []TrackSpec) error {
	ftyp := &mp4.Ftyp{MajorBrand: brand, MinorVersion: 1}
	ftyp.AddCompatibleBrand(brand)
	var fb bytes.Buffer
	if _, err := mp4.Marshal(&fb, ftyp, mp4.Context{}); err != nil {
		return err
	}
	ftypBytes := make([]byte, 8, 8+fb.Len())
	binary.BigEndian.PutUint32(ftypBytes[0:4], uint32(8+fb.Len()))
	copy(ftypBytes[4:8], "ftyp")
	ftypBytes = append(ftypBytes, fb.Bytes()...)
	mdatPayloadStart := int64(len(ftypBytes)) + 8

	offsets := make([][]uint32, len(specs))
	cursor := mdatPayloadStart
	for ti := range specs {
		for i := range specs[ti].Deltas {
			offsets[ti] = append(offsets[ti], uint32(cursor))
			cursor += int64(specs[ti].size(i))
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(ftypBytes); err != nil {
		return err
	}
	var totalPayload int64
	for ti := range specs {
		for i := range specs[ti].Deltas {
			totalPayload += int64(specs[ti].payload(i))
		}
	}
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(8+totalPayload))
	copy(hdr[4:], "mdat")
	if _, err := f.Write(hdr[:]); err != nil {
		return err
	}
	for ti := range specs {
		for i := range specs[ti].Deltas {
			payload := make([]byte, specs[ti].payload(i))
			for j := range payload {
				payload[j] = byte(ti*17 + i*31 + j*7)
			}
			if _, err := f.Write(payload); err != nil {
				return err
			}
		}
	}

	w := mp4.NewWriter(f)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	var mvhdDur uint64
	for ti := range specs {
		var d uint64
		for _, dlt := range specs[ti].Deltas {
			d += uint64(dlt)
		}
		if ms := d * 1000 / uint64(specs[ti].Timescale); ms > mvhdDur {
			mvhdDur = ms
		}
	}
	if err := writeBox(w, &mp4.Mvhd{
		Timescale: 1000, DurationV0: uint32(mvhdDur),
		Rate: 0x00010000, Volume: 0x0100,
		Matrix:      [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		NextTrackID: uint32(len(specs) + 1),
	}); err != nil {
		return err
	}
	for ti := range specs {
		if err := writeTrak(w, &specs[ti], uint32(ti+1), offsets[ti]); err != nil {
			return err
		}
	}
	_, err = w.EndBox() // moov
	return err
}

func writeTrak(w *mp4.Writer, spec *TrackSpec, trackID uint32, stco []uint32) error {
	start := func(t mp4.BoxType) error { _, err := w.StartBox(&mp4.BoxInfo{Type: t}); return err }
	end := func() error { _, err := w.EndBox(); return err }
	marshal := func(b mp4.IImmutableBox) error { return writeBox(w, b) }

	var dur uint64
	for _, d := range spec.Deltas {
		dur += uint64(d)
	}
	matrix := [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
	if err := start(mp4.BoxTypeTrak()); err != nil {
		return err
	}
	if err := marshal(&mp4.Tkhd{TrackID: trackID, DurationV0: uint32(dur), Matrix: matrix}); err != nil {
		return err
	}
	if err := start(mp4.BoxTypeMdia()); err != nil {
		return err
	}
	if err := marshal(&mp4.Mdhd{Timescale: spec.Timescale, DurationV0: uint32(dur),
		Language: [3]byte{'u', 'n', 'd'}}); err != nil {
		return err
	}
	var htype [4]byte
	copy(htype[:], spec.Handler)
	if err := marshal(&mp4.Hdlr{HandlerType: htype, Name: "fixture"}); err != nil {
		return err
	}
	if err := start(mp4.BoxTypeMinf()); err != nil {
		return err
	}
	if spec.Handler == "soun" {
		if err := marshal(&mp4.Smhd{}); err != nil {
			return err
		}
	} else {
		if err := marshal(&mp4.Vmhd{}); err != nil {
			return err
		}
	}
	if err := start(mp4.BoxTypeStbl()); err != nil {
		return err
	}
	if err := marshal(&mp4.Stsd{}); err != nil {
		return err
	}
	sttsEntries := rleDeltas(spec.Deltas)
	if err := marshal(&mp4.Stts{EntryCount: uint32(len(sttsEntries)), Entries: sttsEntries}); err != nil {
		return err
	}
	if spec.CTTS != nil {
		if err := marshal(makeCTTS(spec.CTTS)); err != nil {
			return err
		}
	}
	if err := marshal(&mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{
		{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1},
	}}); err != nil {
		return err
	}
	sizes := make([]uint32, len(spec.Deltas))
	for i := range sizes {
		sizes[i] = spec.size(i)
	}
	if err := marshal(&mp4.Stsz{SampleCount: uint32(len(sizes)), EntrySize: sizes}); err != nil {
		return err
	}
	if err := marshal(&mp4.Stco{EntryCount: uint32(len(stco)), ChunkOffset: stco}); err != nil {
		return err
	}
	if err := end(); err != nil { // stbl
		return err
	}
	if err := end(); err != nil { // minf
		return err
	}
	if err := end(); err != nil { // mdia
		return err
	}
	return end() // trak
}

func rleDeltas(deltas []uint32) []mp4.SttsEntry {
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

func makeCTTS(offsets []int32) *mp4.Ctts {
	version := uint8(0)
	for _, o := range offsets {
		if o < 0 {
			version = 1
		}
	}
	c := &mp4.Ctts{}
	c.SetVersion(version)
	for _, o := range offsets {
		if n := len(c.Entries); n > 0 {
			last := &c.Entries[n-1]
			cur := int32(last.SampleOffsetV0)
			if version == 1 {
				cur = last.SampleOffsetV1
			}
			if cur == o {
				last.SampleCount++
				continue
			}
		}
		e := mp4.CttsEntry{SampleCount: 1}
		if version == 1 {
			e.SampleOffsetV1 = o
		} else {
			e.SampleOffsetV0 = uint32(o)
		}
		c.Entries = append(c.Entries, e)
	}
	c.EntryCount = uint32(len(c.Entries))
	return c
}
