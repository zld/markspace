package ingest

import (
	"bytes"
	"fmt"

	mp4 "github.com/abema/go-mp4"

	"mediaroute/internal/model"
)

// parsedTraf is the intermediate per-traf parse result before timeline repair.
type parsedTraf struct {
	trackID       uint32
	tfhdDefaults  TrackDefaults
	tfdtPresent   bool
	tfdtValue     uint64
	tfdtVersion   uint8
	tfdtPatchAt   uint64 // absolute file offset of the base decode time field
	samples       []model.Sample
	firstOffset   uint64 // absolute source offset of the first sample byte
	durationTicks uint64
}

// ParseMoof extracts mfhd sequence, per-traf tfdt and sample layout from one
// moof payload. moofOffset is the absolute offset of the moof box header and
// payloadOffset the absolute offset of its first payload byte; patch offsets
// returned are absolute file offsets.
func ParseMoof(payload []byte, moofOffset, payloadOffset uint64, defaults map[uint32]TrackDefaults) (seq uint32, seqKnown bool, trafs []parsedTraf, err error) {
	r := bytes.NewReader(payload)
	var cur *parsedTraf
	var trunCursor uint64 // running absolute data cursor inside a traf
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoof(), mp4.BoxTypeTraf():
			_, err := h.Expand()
			return nil, err
		}
		if !h.BoxInfo.IsSupportedType() {
			return nil, nil
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		bi := h.BoxInfo
		switch b := box.(type) {
		case *mp4.Mfhd:
			seq = b.SequenceNumber
			seqKnown = true
		case *mp4.Tfhd:
			t := parsedTraf{trackID: b.TrackID}
			if d, ok := defaults[b.TrackID]; ok {
				t.tfhdDefaults = d
			}
			if b.CheckFlag(mp4.TfhdDefaultSampleDurationPresent) {
				t.tfhdDefaults.Duration = b.DefaultSampleDuration
				t.tfhdDefaults.Present = true
			}
			if b.CheckFlag(mp4.TfhdDefaultSampleSizePresent) {
				t.tfhdDefaults.Size = b.DefaultSampleSize
			}
			if b.CheckFlag(mp4.TfhdDefaultSampleFlagsPresent) {
				t.tfhdDefaults.Flags = b.DefaultSampleFlags
			}
			// Canonical CMAF sets default-base-is-moof; an explicit
			// base_data_offset wins when present.
			if b.CheckFlag(mp4.TfhdBaseDataOffsetPresent) {
				trunCursor = b.BaseDataOffset
			} else {
				trunCursor = moofOffset // default-base-is-moof
			}
			trafs = append(trafs, t)
			cur = &trafs[len(trafs)-1]
		case *mp4.Tfdt:
			if cur == nil {
				return nil, nil
			}
			cur.tfdtPresent = true
			cur.tfdtVersion = b.GetVersion()
			cur.tfdtValue = b.GetBaseMediaDecodeTime()
			// The payload starts with 4 bytes of FullBox fields, then the
			// baseMediaDecodeTime value this module may rewrite.
			cur.tfdtPatchAt = payloadOffset + bi.Offset + bi.HeaderSize + 4
		case *mp4.Trun:
			if cur == nil {
				return nil, nil
			}
			base := trunCursor
			if b.CheckFlag(0x000001) {
				base = uint64(int64(trunCursor) + int64(b.DataOffset))
			}
			cursor := base
			for i := 0; i < int(b.SampleCount) && i < len(b.Entries); i++ {
				e := b.Entries[i]
				s := model.Sample{
					Duration:     cur.tfhdDefaults.Duration,
					Size:         cur.tfhdDefaults.Size,
					Flags:        cur.tfhdDefaults.Flags,
					SourceOffset: cursor,
				}
				if b.CheckFlag(0x000100) {
					s.Duration = e.SampleDuration
				}
				if b.CheckFlag(0x000200) {
					s.Size = e.SampleSize
				}
				if b.CheckFlag(0x000400) {
					s.Flags = e.SampleFlags
				}
				if b.CheckFlag(0x000004) && i == 0 {
					s.Flags = b.FirstSampleFlags
				}
				if b.CheckFlag(0x000800) {
					if b.GetVersion() == 0 {
						s.Composition = int32(e.SampleCompositionTimeOffsetV0)
					} else {
						s.Composition = e.SampleCompositionTimeOffsetV1
					}
				}
				s.IsSync = sampleIsSync(s.Flags)
				if s.Size > 0 {
					s.SourceOffset = cursor
				}
				if len(cur.samples) == 0 {
					cur.firstOffset = cursor
				}
				cursor += uint64(s.Size)
				cur.durationTicks += uint64(s.Duration)
				cur.samples = append(cur.samples, s)
			}
			trunCursor = cursor
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		return 0, false, nil, fmt.Errorf("parse moof at %d: %w", moofOffset, err)
	}
	return seq, seqKnown, trafs, nil
}

// sampleIsSync applies ISO/IEC 14496-12 sample flag semantics: a sample is a
// random access point when it is not flagged as a non-sync sample and its
// dependency is "does not depend on others" (or unspecified).
func sampleIsSync(flags uint32) bool {
	if flags&0x00010000 != 0 { // sample_is_non_sync_sample
		return false
	}
	dependsOn := (flags >> 24) & 0x3
	return dependsOn != 1
}

// ExistingIndex is a hint parsed from an existing sidx or mfra. It is never
// trusted: every referenced offset is rechecked against real box boundaries.
type ExistingIndex struct {
	Present    bool
	SidxOffset uint64
	References []SidxReference
	Tfras      []TfraReference
	BadOffsets []uint64
}

// SidxReference mirrors one sidx subsegment reference.
type SidxReference struct {
	Offset   uint64
	Size     uint32
	Duration uint32
	StartSAP bool
}

// TfraReference mirrors one tfra entry.
type TfraReference struct {
	TrackID    uint32
	Time       uint64
	MoofOffset uint64
}

// ParseExistingIndex reads sidx boxes and mfra/tfra entries from the
// top-level box list. Offsets are checked later by the index stage.
func ParseExistingIndex(boxes []TopBox) ExistingIndex {
	var out ExistingIndex
	for _, tb := range boxes {
		switch tb.Header.Type {
		case "sidx":
			refs, ok := parseSidx(tb.Payload, tb.Header.PayloadOffset())
			if ok {
				out.Present = true
				out.SidxOffset = tb.Header.Offset
				out.References = append(out.References, refs...)
			}
		case "mfra":
			tfras, ok := parseMfra(tb.Payload)
			if ok {
				out.Present = true
				out.Tfras = append(out.Tfras, tfras...)
			}
		}
	}
	return out
}

func parseSidx(payload []byte, payloadOffset uint64) ([]SidxReference, bool) {
	var box mp4.Sidx
	if _, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), &box, mp4.Context{}); err != nil {
		return nil, false
	}
	// First reference starts at the sidx end plus first_offset.
	cursor := payloadOffset + uint64(len(payload))
	if box.GetVersion() == 0 {
		cursor += uint64(box.FirstOffsetV0)
	} else {
		cursor += box.FirstOffsetV1
	}
	refs := make([]SidxReference, 0, len(box.References))
	for _, r := range box.References {
		refs = append(refs, SidxReference{
			Offset:   cursor,
			Size:     r.ReferencedSize,
			Duration: r.SubsegmentDuration,
			StartSAP: r.StartsWithSAP,
		})
		cursor += uint64(r.ReferencedSize)
	}
	return refs, true
}

func parseMfra(payload []byte) ([]TfraReference, bool) {
	type tfraEntry struct {
		trackID uint32
		time    uint64
		moof    uint64
	}
	var entries []tfraEntry
	r := bytes.NewReader(payload)
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMfra():
			_, err := h.Expand()
			return nil, err
		}
		if h.BoxInfo.Type != mp4.BoxTypeTfra() || !h.BoxInfo.IsSupportedType() {
			return nil, nil
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		tfra, ok := box.(*mp4.Tfra)
		if !ok {
			return nil, nil
		}
		for i := 0; i < int(tfra.NumberOfEntry); i++ {
			entries = append(entries, tfraEntry{
				trackID: tfra.TrackID,
				time:    tfra.GetTime(i),
				moof:    tfra.GetMoofOffset(i),
			})
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		return nil, false
	}
	out := make([]TfraReference, 0, len(entries))
	for _, e := range entries {
		out = append(out, TfraReference{TrackID: e.trackID, Time: e.time, MoofOffset: e.moof})
	}
	return out, true
}
