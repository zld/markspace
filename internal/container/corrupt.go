package container

import (
	"encoding/binary"

	"archivist/internal/model"
)

// applyCorruption mutates the serialized byte stream after generation. Every
// corruption is localized and leaves deterministic evidence in the file.
func (g *generator) applyCorruption() {
	switch g.spec.Corruption {
	case CorruptTruncatedBox:
		g.corruptTruncated()
	case CorruptSampleTable:
		g.corruptSampleTable()
	case CorruptMissingKeyframe:
		// structural: stss was already omitted in writeTrak
	case CorruptAudioOffset:
		// structural: edit list was already emitted in writeTrak
	case CorruptKeyframeDrift:
		// structural: stss entries already shifted for the high track
	default:
	}
}

// corruptTruncated shortens the final mdat box by dropping the payload of the
// last sample of the last track, while leaving its declared box size intact.
// The parser detects the overrun against EOF and can recover by trimming the
// dangling sample (its stco chunk points past readable content).
func (g *generator) corruptTruncated() {
	last := g.spec.Tracks[len(g.spec.Tracks)-1]
	drop := g.sizes[last.ID][last.SampleCount-1]
	data := g.out.Bytes()
	cut := len(data) - int(drop)
	g.out = Writer{}
	g.out.buf.Write(data[:cut])
}

// corruptSampleTable rewrites the first track's first stco chunk offset so it
// points beyond the file. This is an un-recoverable contradiction: the index
// cannot be rebuilt from bitstream content because declared geometry is bad.
func (g *generator) corruptSampleTable() {
	data := g.out.Bytes()
	res, err := Parse(data)
	if err != nil {
		return // leave data untouched; later stages report structural failure
	}
	stco, ok := findStcoOffset(res)
	if !ok {
		return
	}
	// stco payload: fullbox(4) + entry_count(4) + first offset(4)
	firstEntry := stco + 8 + 4 + 4
	if firstEntry+4 > len(data) {
		return
	}
	binary.BigEndian.PutUint32(data[firstEntry:firstEntry+4], uint32(len(data))+4096)
}

// findStcoOffset returns the absolute header offset of the first stco box.
func findStcoOffset(res *ParseResult) (int, bool) {
	moov := FindFirst(res.Boxes, BoxMoov)
	if moov == nil {
		return 0, false
	}
	for _, trak := range FindAll(moov.Children, BoxTrak) {
		if off := stcoInTrak(trak); off >= 0 {
			return off, true
		}
	}
	return 0, false
}

func stcoInTrak(trak *Box) int {
	mdia := FindFirst(trak.Children, BoxMdia)
	if mdia == nil {
		return -1
	}
	minf := FindFirst(mdia.Children, BoxMinf)
	if minf == nil {
		return -1
	}
	stbl := FindFirst(minf.Children, BoxStbl)
	if stbl == nil {
		return -1
	}
	if stco := FindFirst(stbl.Children, BoxStco); stco != nil {
		return int(stco.Offset)
	}
	if co64 := FindFirst(stbl.Children, BoxCo64); co64 != nil {
		return int(co64.Offset)
	}
	return -1
}

// Corruptable reports whether a corruption kind is recognized.
func Corruptable(kind string) bool {
	switch kind {
	case CorruptNone, CorruptTruncatedBox, CorruptSampleTable,
		CorruptMissingKeyframe, CorruptAudioOffset, CorruptKeyframeDrift:
		return true
	}
	return false
}

// Recoverability classifies a corruption for reporting.
func Recoverability(kind string) (recoverable bool, stage string) {
	switch kind {
	case CorruptTruncatedBox, CorruptMissingKeyframe, CorruptAudioOffset:
		return true, model.StageIndex
	case CorruptSampleTable, CorruptKeyframeDrift:
		return false, model.StageManifest
	default:
		return true, ""
	}
}
