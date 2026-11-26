// Package fixture generates self-contained CMAF/fMP4 datasets for the
// regression tool. No external media is used: every box is synthesized
// deterministically from a seed, and five defect shapes can be injected
// (clean, mfhd gap, tfdt rollback, mdat truncation, RTP wraparound).
package fixture

import (
	"encoding/binary"

	"cmafrepair/pkg/media/cmaf"
)

// Geometry fixes the shape of every generated dataset.
const (
	FragmentsPerRep = 24
	SamplesPerFrag  = 1
	FragmentMs      = 200 // each fragment spans 200 ms
	SyncFlags       = uint32(0x02000000)
)

// box is a small local alias so the fixture owns its serialization even if
// the cmaf BoxBuilder API evolves.
type box = cmaf.BoxBuilder

// initSegment builds a minimal ftyp+moov init segment for one track.
func initSegment(timescale, trexDuration uint32) []byte {
	mdhd := cmaf.NewBox("mdhd").FullHeader(0, 0).
		U32(0).U32(0).U32(timescale).U32(0).
		Bytes([]byte{0, 0, 0, 0, 0, 0})
	tkhd := cmaf.NewBox("tkhd").FullHeader(0, 0).
		U32(0).U32(0).U32(1).Bytes(make([]byte, 4))
	mdia := cmaf.NewBox("mdia").Child(mdhd)
	trak := cmaf.NewBox("trak").Child(tkhd).Child(mdia)
	trex := cmaf.NewBox("trex").FullHeader(0, 0).
		U32(1).U32(1).U32(trexDuration).U32(0).U32(0)
	mvex := cmaf.NewBox("mvex").Child(trex)
	moov := cmaf.NewBox(cmaf.BoxMoov).Child(trak).Child(mvex)
	ftyp := cmaf.NewBox(cmaf.BoxFtyp).Bytes([]byte("iso5dash"))
	return append(ftyp.Build(), moov.Build()...)
}

// fragmentSpec parameterizes one media fragment.
type fragmentSpec struct {
	sequence  uint32
	tfdt      uint64
	tfdtVer   byte
	duration  uint32
	size      uint32
	syncFlags uint32
}

// mediaFragment assembles a moof+mdat fragment from a spec. The single trun
// entry carries duration, size and per-sample flags (trun flags 0x700).
func mediaFragment(s fragmentSpec) []byte {
	trun := cmaf.NewBox(cmaf.BoxTrun).FullHeader(1, 0x000700).
		U32(1). // sample count
		U32(s.duration).U32(s.size).U32(s.syncFlags)
	tfhd := cmaf.NewBox(cmaf.BoxTfhd).FullHeader(0, 0).U32(1)
	tfdtBox := cmaf.NewBox(cmaf.BoxTfdt).FullHeader(s.tfdtVer, 0)
	if s.tfdtVer == 1 {
		tfdtBox.U64(s.tfdt)
	} else {
		tfdtBox.U32(uint32(s.tfdt))
	}
	mfhd := cmaf.NewBox(cmaf.BoxMfhd).FullHeader(0, 0).U32(s.sequence)
	traf := cmaf.NewBox(cmaf.BoxTraf).Child(tfhd).Child(tfdtBox).Child(trun)
	moof := cmaf.NewBox(cmaf.BoxMoof).Child(mfhd).Child(traf)
	mdat := cmaf.NewBox(cmaf.BoxMdat).Bytes(make([]byte, s.size))
	return append(moof.Build(), mdat.Build()...)
}

// patchMfhdSequence overwrites the mfhd sequence-number field in a serialized
// fragment without rebuilding it, mirroring a real on-disk corruption.
func patchMfhdSequence(data []byte, newSeq uint32) {
	off := locateMfhdValue(data)
	if off >= 0 {
		binary.BigEndian.PutUint32(data[off:off+4], newSeq)
	}
}

// patchTfdtValue overwrites the tfdt base-media-decode-time field. The tfdt
// box is built as version 1 (8-byte value) by mediaFragment.
func patchTfdtValue(data []byte, newTfdt uint64) {
	off := locateTfdtValue(data)
	if off >= 0 {
		binary.BigEndian.PutUint64(data[off:off+8], newTfdt)
	}
}

// locateMfhdValue returns the absolute offset of the mfhd sequence field
// (after the 4-byte full-box header).
func locateMfhdValue(data []byte) int64 {
	off := findBoxValueOffset(data, cmaf.BoxMfhd)
	if off < 0 {
		return -1
	}
	return int64(off + 4)
}

// locateTfdtValue returns the absolute offset of the tfdt value field.
func locateTfdtValue(data []byte) int64 {
	off := findBoxValueOffset(data, cmaf.BoxTfdt)
	if off < 0 {
		return -1
	}
	// full-box header (4 bytes) precedes the value.
	return int64(off + 4)
}

// findBoxValueOffset walks top-level + one level of nested boxes to locate a
// target box, returning the offset of its payload.
func findBoxValueOffset(data []byte, target string) int {
	type frame struct {
		start, end int
	}
	stack := []frame{{0, len(data)}}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		pos := cur.start
		for pos+8 <= cur.end {
			size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
			typ := string(data[pos+4 : pos+8])
			if size < 8 || pos+size > cur.end {
				return -1
			}
			if typ == target {
				return pos + 8
			}
			// Descend into known containers.
			if typ == cmaf.BoxMoof || typ == cmaf.BoxTraf {
				stack = append(stack, frame{pos + 8, pos + size})
			}
			pos += size
		}
	}
	return -1
}

// truncateMdat cuts trailing bytes so the mdat box promises more than the
// file now holds.
func truncateMdat(data []byte, drop int) []byte {
	if drop >= len(data) {
		drop = len(data) - 1
	}
	return data[:len(data)-drop]
}
