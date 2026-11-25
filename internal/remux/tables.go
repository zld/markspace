// Package remux writes mapped events into a new MP4 file. Sample payload
// bytes are copied verbatim; only container structure is rebuilt. Every
// written event gets its output byte offset recorded for the manifest.
package remux

import (
	"bytes"
	"encoding/binary"

	"github.com/abema/go-mp4"

	"mediacore/internal/clock"
	"mediacore/internal/model"
)

func buildFtyp() []byte {
	var m memSeeker
	w := mp4.NewWriter(&m)
	ftyp := &mp4.Ftyp{MajorBrand: [4]byte{'i', 's', 'o', 'm'}, MinorVersion: 1}
	ftyp.AddCompatibleBrand([4]byte{'i', 's', 'o', 'm'})
	ftyp.AddCompatibleBrand([4]byte{'a', 'v', 'c', '1'})
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()})
	mp4.Marshal(w, ftyp, mp4.Context{})
	w.EndBox()
	return m.buf
}

func buildEMSG(emsgIdx []int, events []model.Event) []byte {
	var m memSeeker
	w := mp4.NewWriter(&m)
	for _, idx := range emsgIdx {
		e := events[idx]
		box := &mp4.Emsg{
			SchemeIdUri:      "urn:mediacore:emsg",
			Value:            "remux",
			Timescale:        clock.UnifiedTimescale,
			PresentationTime: clock.Convert(uint64(max64(e.Ticks, 0)), clock.UnifiedTimescale, clock.UnifiedTimescale),
			EventDuration:    uint32(max64(e.Duration, 0)),
			Id:               uint32(idx),
			MessageData:      e.Payload,
		}
		box.Version = 1
		w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeEmsg()})
		mp4.Marshal(w, box, mp4.Context{})
		w.EndBox()
	}
	return m.buf
}

func writeStbl(w *mp4.Writer, marshal func(mp4.IImmutableBox) error, o model.OutputTrack, idxs []int, events []model.Event, chunkOff uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	// stsd: fullbox header + entry count + one minimal sample entry.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	var hdr [12]byte // 4 fullbox + 4 entry count + 4? no: 4+4, then entry box
	hdr[7] = 1       // entry count = 1
	if _, err := w.Write(hdr[:8]); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: entryTypeFor(o.Kind)}); err != nil {
		return err
	}
	entry := make([]byte, 8) // 6 reserved + data_reference_index
	entry[7] = 1
	if _, err := w.Write(entry); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // sample entry
		return err
	}
	if _, err := w.EndBox(); err != nil { // stsd
		return err
	}
	// stts from actual per-sample durations (VFR-safe).
	var stts mp4.Stts
	for _, idx := range idxs {
		d := uint32(clock.Convert(uint64(max64(events[idx].Duration, 0)), clock.UnifiedTimescale, o.Timescale))
		if n := len(stts.Entries); n > 0 && stts.Entries[n-1].SampleDelta == d {
			stts.Entries[n-1].SampleCount++
		} else {
			stts.Entries = append(stts.Entries, mp4.SttsEntry{SampleCount: 1, SampleDelta: d})
		}
	}
	stts.EntryCount = uint32(len(stts.Entries))
	if err := marshal(&stts); err != nil {
		return err
	}
	stsc := &mp4.Stsc{Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(len(idxs)), SampleDescriptionIndex: 1}}}
	stsc.EntryCount = 1
	if len(idxs) == 0 {
		stsc.EntryCount = 0
		stsc.Entries = nil
	}
	if err := marshal(stsc); err != nil {
		return err
	}
	stsz := &mp4.Stsz{SampleCount: uint32(len(idxs))}
	for _, idx := range idxs {
		stsz.EntrySize = append(stsz.EntrySize, events[idx].Size)
	}
	if err := marshal(stsz); err != nil {
		return err
	}
	stco := &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{chunkOff}}
	if len(idxs) == 0 {
		stco.EntryCount = 0
		stco.ChunkOffset = nil
	}
	if err := marshal(stco); err != nil {
		return err
	}
	_, err := w.EndBox() // stbl
	return err
}

// writeChpl emits moov/udta/chpl with chapter marks in 100 ns units.
func writeChpl(w *mp4.Writer, chapterIdx []int, events []model.Event) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUdta()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("chpl")}); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.Write([]byte{1, 0, 0, 0, 0, 0, 0, 0}) // version+flags+reserved
	binary.Write(&buf, binary.BigEndian, uint16(len(chapterIdx)))
	for _, idx := range chapterIdx {
		start := clock.Convert(uint64(max64(events[idx].Ticks, 0)), clock.UnifiedTimescale, 10000)
		binary.Write(&buf, binary.BigEndian, start)
		title := events[idx].Payload
		if len(title) > 255 {
			title = title[:255]
		}
		buf.WriteByte(byte(len(title)))
		buf.Write(title)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // chpl
		return err
	}
	_, err := w.EndBox() // udta
	return err
}

func writeMediaHeader(w *mp4.Writer, kind model.TrackKind) error {
	var bi mp4.BoxInfo
	switch kind {
	case model.TrackVideo:
		bi.Type = mp4.BoxTypeVmhd()
	case model.TrackAudio:
		bi.Type = mp4.BoxTypeSmhd()
	default:
		bi.Type = mp4.StrToBoxType("nmhd")
	}
	if _, err := w.StartBox(&bi); err != nil {
		return err
	}
	if _, err := w.Write(make([]byte, 12)); err != nil { // fullbox + reserved
		return err
	}
	_, err := w.EndBox()
	return err
}
