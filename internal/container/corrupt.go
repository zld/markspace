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

// corruptSampleTable rewrites one stco entry so a chunk offset points beyond
// the file. This is an un-recoverable contradiction: the index cannot be
// rebuilt from bitstream content because the declared geometry is invalid.
func (g *generator) corruptSampleTable() {
	data := g.out.Bytes()
	// locate the first track's stco: search for the FourCC inside moov.
	idx := findBoxBytes(data, BoxStco, 0)
	if idx < 0 {
		return
	}
	payloadStart := idx + 8 + 4 // box header + fullbox prefix + entry_count
	if payloadStart+4 > len(data) {
		return
	}
	binary.BigEndian.PutUint32(data[payloadStart:payloadStart+4],
		uint32(len(data))+4096)
}

// findBoxBytes returns the absolute offset of the first box header matching
// t at or after from, or -1.
func findBoxBytes(data []byte, t FourCC, from int) int {
	for i := from; i+8 <= len(data); i++ {
		if data[i+4] == t[0] && data[i+5] == t[1] && data[i+6] == t[2] && data[i+7] == t[3] {
			return i
		}
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
