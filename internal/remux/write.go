// Package remux writes mapped events into a new MP4 file. Sample payload
// bytes are copied verbatim; only container structure is rebuilt. Every
// written event gets its output byte offset recorded for the manifest.
package remux

import (
	"os"
	"sort"

	"github.com/abema/go-mp4"

	"mediacore/internal/model"
)

// WriteMP4 remuxes mapped events (sorted by maptrack.Apply) into path.
// events[i].OutOffset is filled in for every event. Payload bytes are
// never modified.
func WriteMP4(path string, outs []model.OutputTrack, events []model.Event) error {
	outByID := map[string]model.OutputTrack{}
	for _, o := range outs {
		outByID[o.ID] = o
	}
	// Group sample events per output track, preserving sorted order.
	samples := map[string][]int{} // out ID -> event indices
	var chapterIdx, emsgIdx []int
	for i := range events {
		e := &events[i]
		if _, ok := outByID[e.OutTrackID]; !ok {
			return model.NewError(model.ErrMapping, model.StageRemux, e.TrackID, e.SrcOffset,
				"event targets unknown output track %q", e.OutTrackID)
		}
		switch {
		case isSampleKind(e.Kind):
			samples[e.OutTrackID] = append(samples[e.OutTrackID], i)
		case e.Kind == model.EventChapter:
			chapterIdx = append(chapterIdx, i)
		case e.Kind == model.EventEMSG:
			emsgIdx = append(emsgIdx, i)
		}
	}
	// Drop output tracks with no samples; they would carry empty tables.
	active := make([]model.OutputTrack, 0, len(outs))
	for _, o := range outs {
		if len(samples[o.ID]) > 0 {
			active = append(active, o)
		}
	}
	outs = active
	// Pass 1: measure moov with placeholder chunk offsets.
	stco := map[string]uint32{}
	for _, o := range outs {
		stco[o.ID] = 0
	}
	moov0, err := buildMoov(outs, samples, events, chapterIdx, stco)
	if err != nil {
		return err
	}
	ftyp := buildFtyp()
	emsgBytes := buildEMSG(emsgIdx, events)
	// Compute chunk offsets: ftyp | moov | emsg* | mdat header | data.
	dataStart := uint64(len(ftyp)) + uint64(len(moov0)) + uint64(len(emsgBytes)) + 8
	chunkOff := map[string]uint32{}
	cur := dataStart
	for _, o := range outs {
		chunkOff[o.ID] = uint32(cur)
		for _, idx := range samples[o.ID] {
			cur += uint64(events[idx].Size)
		}
	}
	moov, err := buildMoov(outs, samples, events, chapterIdx, chunkOff)
	if err != nil {
		return err
	}
	if len(moov) != len(moov0) {
		return model.NewError(model.ErrIO, model.StageRemux, "", -1, "moov size changed between passes")
	}
	// Assign output offsets to sample events.
	pos := map[string]uint64{}
	for _, o := range outs {
		pos[o.ID] = uint64(chunkOff[o.ID])
	}
	for i := range events {
		if isSampleKind(events[i].Kind) {
			events[i].OutOffset = int64(pos[events[i].OutTrackID])
			pos[events[i].OutTrackID] += uint64(events[i].Size)
		}
	}
	assignMetaOffsets(events, emsgIdx, chapterIdx, uint64(len(ftyp)), uint64(len(moov)))
	// Write the file.
	f, err := os.Create(path)
	if err != nil {
		return model.NewError(model.ErrIO, model.StageRemux, "", -1, "create: %v", err)
	}
	defer f.Close()
	if _, err := f.Write(ftyp); err != nil {
		return model.NewError(model.ErrIO, model.StageRemux, "", -1, "write ftyp: %v", err)
	}
	if _, err := f.Write(moov); err != nil {
		return model.NewError(model.ErrIO, model.StageRemux, "", -1, "write moov: %v", err)
	}
	if _, err := f.Write(emsgBytes); err != nil {
		return model.NewError(model.ErrIO, model.StageRemux, "", -1, "write emsg: %v", err)
	}
	w := mp4.NewWriter(f)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return model.NewError(model.ErrIO, model.StageRemux, "", -1, "mdat: %v", err)
	}
	for _, o := range outs {
		for _, idx := range samples[o.ID] {
			if _, err := w.Write(events[idx].Payload); err != nil {
				return model.NewError(model.ErrIO, model.StageRemux, events[idx].OutTrackID,
					events[idx].SrcOffset, "write sample: %v", err)
			}
		}
	}
	if _, err := w.EndBox(); err != nil {
		return model.NewError(model.ErrIO, model.StageRemux, "", -1, "mdat end: %v", err)
	}
	return nil
}

// assignMetaOffsets records output offsets for emsg/chapter events and
// resolves SEI events to their containing sample's output position.
func assignMetaOffsets(events []model.Event, emsgIdx, chapterIdx []int, ftypLen, moovLen uint64) {
	base := ftypLen + moovLen
	off := base
	for _, idx := range emsgIdx {
		events[idx].OutOffset = int64(off)
		off += uint64(8+16+4+4+len(events[idx].Payload)) + uint64(len("urn:mediacore:emsg")) + 2
	}
	// chpl lives inside moov/udta; exact position is not tracked per
	// chapter, so point at the moov box itself.
	for _, idx := range chapterIdx {
		events[idx].OutOffset = int64(ftypLen)
	}
	// SEI: locate containing media sample on the same output track.
	byTrack := map[string][]int{}
	for i := range events {
		if events[i].Kind == model.EventMedia {
			byTrack[events[i].OutTrackID] = append(byTrack[events[i].OutTrackID], i)
		}
	}
	for i := range events {
		if events[i].Kind != model.EventSEI {
			continue
		}
		media := byTrack[events[i].OutTrackID]
		j := sort.Search(len(media), func(k int) bool {
			return events[media[k]].SrcOffset > events[i].SrcOffset
		}) - 1
		if j >= 0 {
			m := events[media[j]]
			events[i].OutOffset = m.OutOffset + (events[i].SrcOffset - m.SrcOffset)
		}
	}
}
