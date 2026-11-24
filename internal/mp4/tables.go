package mp4

import (
	"encoding/binary"
	"fmt"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"mediascan/internal/model"
)

// maxTableEntries is a defensive allocation bound for sample tables.
const maxTableEntries = 4_000_000

func unmarshalHeader(f io.ReaderAt, h *BoxHeader, dst gomp4.IBox) error {
	payloadSize := h.Size - h.HeaderSize
	rs := io.NewSectionReader(f, h.PayloadOffset, payloadSize)
	_, err := gomp4.Unmarshal(rs, uint64(payloadSize), dst, gomp4.Context{})
	return err
}

// payloadBytes reads the whole (already bounds-checked) box payload.
func payloadBytes(f io.ReaderAt, h *BoxHeader) ([]byte, error) {
	n := h.Size - h.HeaderSize
	b := make([]byte, n)
	_, err := f.ReadAt(b, h.PayloadOffset)
	return b, err
}

// entryCountFits guards against crafted enormous entry counts that would
// make go-mp4 pre-allocate huge slices. extra is the fixed byte count before
// the entry array and each entry is entryBytes wide.
func entryCountFits(payload []byte, extra, entryBytes int) (uint32, bool) {
	if len(payload) < extra {
		return 0, false
	}
	count := binary.BigEndian.Uint32(payload[extra-4 : extra])
	if count > maxTableEntries {
		return count, false
	}
	need := extra + int(count)*entryBytes
	return count, need <= len(payload)
}

func readTimescale(f io.ReaderAt, hs []BoxHeader, segment string) (uint32, *model.Fault) {
	h := findHeader(hs, "mdhd")
	if h == nil {
		return 0, nil
	}
	var m gomp4.Mdhd
	if err := unmarshalHeader(f, h, &m); err != nil {
		flt := model.NewFault(model.CodeMissingSampleTable, segment,
			fmt.Sprintf("mdhd parse failed at %d: %v", h.Offset, err))
		flt.BoxOffset = h.Offset
		return 0, &flt
	}
	if m.Timescale == 0 {
		flt := model.NewFault(model.CodeZeroTimescale, segment, "mdhd timescale is zero")
		flt.BoxOffset = h.Offset
		flt.OffsetBytes = h.Offset
		return 0, &flt
	}
	return m.Timescale, nil
}

func buildProgressiveTrack(f io.ReaderAt, hs []BoxHeader, ts uint32, segment string) (*Track, []model.Fault) {
	var faults []model.Fault

	sttsH := findHeader(hs, "stts")
	stszH := findHeader(hs, "stsz")
	if sttsH == nil || stszH == nil {
		flt := model.NewFault(model.CodeMissingSampleTable, segment,
			"moov/stbl is missing stts or stsz sample tables")
		if moov := findHeader(hs, "moov"); moov != nil {
			flt.BoxOffset = moov.Offset
			flt.OffsetBytes = moov.Offset
		}
		faults = append(faults, flt)
		return nil, faults
	}

	var stsz gomp4.Stsz
	if b, err := payloadBytes(f, stszH); err == nil {
		if _, ok := entryCountFits(b, 12, 4); !ok && binary.BigEndian.Uint32(b[4:8]) == 0 {
			faults = append(faults, badTableFault(segment, stszH, "stsz sample count exceeds box payload"))
			return nil, faults
		}
	}
	if err := unmarshalHeader(f, stszH, &stsz); err != nil {
		faults = append(faults, badTableFault(segment, stszH, fmt.Sprintf("stsz: %v", err)))
		return nil, faults
	}
	n := int(stsz.SampleCount)
	sizes := make([]uint32, n)
	if stsz.SampleSize != 0 {
		for i := range sizes {
			sizes[i] = stsz.SampleSize
		}
	} else {
		copy(sizes, stsz.EntrySize)
	}

	var stts gomp4.Stts
	if err := unmarshalHeader(f, sttsH, &stts); err != nil {
		faults = append(faults, badTableFault(segment, sttsH, fmt.Sprintf("stts: %v", err)))
		return nil, faults
	}
	deltas := expandDeltas(&stts, n)

	syncSet := map[int]bool{}
	syncIndices := []int64{}
	if stssH := findHeader(hs, "stss"); stssH != nil {
		var stss gomp4.Stss
		if err := unmarshalHeader(f, stssH, &stss); err == nil {
			for _, num := range stss.SampleNumber {
				idx := int(num) - 1
				if idx >= 0 && idx < n {
					syncSet[idx] = true
					syncIndices = append(syncIndices, int64(idx))
				}
			}
		} else {
			faults = append(faults, badTableFault(segment, stssH, fmt.Sprintf("stss: %v", err)))
		}
	} else {
		// ISO semantics: absent stss means every sample is a sync sample.
		for i := 0; i < n; i++ {
			syncSet[i] = true
			syncIndices = append(syncIndices, int64(i))
		}
	}

	offsets := expandOffsets(f, hs, n, sizes, &faults, segment)

	track := &Track{ID: 1, Timescale: ts, SyncIndices: syncIndices}
	var t int64
	for i := 0; i < n; i++ {
		var off int64 = model.Unknown
		if i < len(offsets) {
			off = offsets[i]
		}
		d := uint32(0)
		if i < len(deltas) {
			d = deltas[i]
		}
		s := Sample{
			Index: int64(i), Offset: off, Size: sizes[i], Delta: d,
			TimeSource: t, TimeUnified: toUnified(t, ts, segment),
			DurationTicks: toUnified(int64(d), ts, segment),
			Sync:          syncSet[i],
		}
		track.Samples = append(track.Samples, s)
		t += int64(d)
	}
	return track, faults
}

func expandDeltas(stts *gomp4.Stts, n int) []uint32 {
	out := make([]uint32, 0, n)
	for _, e := range stts.Entries {
		for k := uint32(0); k < e.SampleCount && len(out) < n; k++ {
			out = append(out, e.SampleDelta)
		}
	}
	for len(out) < n {
		out = append(out, 0)
	}
	return out
}

// expandOffsets computes every sample's absolute offset from stsc + stco/co64.
func expandOffsets(f io.ReaderAt, hs []BoxHeader, n int, sizes []uint32, faults *[]model.Fault, segment string) []int64 {
	stscH := findHeader(hs, "stsc")
	stcoH := findHeader(hs, "stco")
	co64H := findHeader(hs, "co64")
	if stscH == nil || (stcoH == nil && co64H == nil) {
		flt := model.NewFault(model.CodeMissingSampleTable, segment, "stsc/stco missing; sample byte offsets unavailable")
		if stscH != nil {
			flt.BoxOffset = stscH.Offset
		}
		*faults = append(*faults, flt)
		return nil
	}
	var stsc gomp4.Stsc
	if err := unmarshalHeader(f, stscH, &stsc); err != nil {
		*faults = append(*faults, badTableFault(segment, stscH, fmt.Sprintf("stsc: %v", err)))
		return nil
	}
	var chunkOff []int64
	if stcoH != nil {
		var stco gomp4.Stco
		if err := unmarshalHeader(f, stcoH, &stco); err != nil {
			*faults = append(*faults, badTableFault(segment, stcoH, fmt.Sprintf("stco: %v", err)))
			return nil
		}
		for _, o := range stco.ChunkOffset {
			chunkOff = append(chunkOff, int64(o))
		}
	} else {
		var co64 gomp4.Co64
		if err := unmarshalHeader(f, co64H, &co64); err != nil {
			*faults = append(*faults, badTableFault(segment, co64H, fmt.Sprintf("co64: %v", err)))
			return nil
		}
		for _, o := range co64.ChunkOffset {
			chunkOff = append(chunkOff, int64(o))
		}
	}

	out := make([]int64, 0, n)
	sampleIdx := 0
	for chunk := 0; chunk < len(chunkOff) && sampleIdx < n; chunk++ {
		perChunk := uint32(0)
		for _, e := range stsc.Entries {
			if int(e.FirstChunk)-1 <= chunk {
				perChunk = e.SamplesPerChunk
			}
		}
		pos := int64(0)
		for k := uint32(0); k < perChunk && sampleIdx < n; k++ {
			out = append(out, chunkOff[chunk]+pos)
			if sampleIdx < len(sizes) {
				pos += int64(sizes[sampleIdx])
			}
			sampleIdx++
		}
	}
	return out
}

func badTableFault(segment string, h *BoxHeader, msg string) model.Fault {
	flt := model.NewFault(model.CodeIllegalBoxLength, segment, msg)
	flt.BoxType = h.Type
	flt.BoxOffset = h.Offset
	flt.OffsetBytes = h.Offset
	return flt
}
