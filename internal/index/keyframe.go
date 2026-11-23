// Package index validates per-track keyframe indexes stored inside MP4
// containers. The platform-specific 'kidx' box (written and read through
// github.com/abema/go-mp4) holds keyframe sample numbers and per-entry
// checksums; corrupted indexes are rejected with a phase error and never
// silently repaired.
package index

import (
	"encoding/binary"
	"hash/crc32"
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediaroute/internal/clock"
	"mediaroute/internal/codec"
)

// kidxMagic identifies a mediaroute keyframe index box.
var kidxMagic = [4]byte{'k', 'i', 'd', 'x'}

// kidxBoxType is the custom MP4 box type registered with go-mp4.
var kidxBoxType = mp4.BoxType{'k', 'i', 'd', 'x'}

const (
	kidxVersion = 1
	kidxHeader  = 4 + 1 // magic + version
	kidxEntry   = 4 + 4 // sample number + crc32
)

// Entry is one validated keyframe index record.
type Entry struct {
	Sample uint32 // 1-based sample number within the track
	CRC    uint32 // crc32 over the declared sample number bytes
}

// KidxBox is the custom box payload. It implements mp4.IAnyType so go-mp4
// can marshal and unmarshal it like any standard box. Each entry is one
// fixed 64-bit word: upper 32 bits are the sample number, lower 32 its CRC,
// which keeps a single dynamic-length array (the layout go-mp4 supports).
type KidxBox struct {
	mp4.Box
	Magic      [4]byte `mp4:"0,size=8"`
	Version    uint8   `mp4:"1,size=8"`
	EntryCount uint32  `mp4:"2,size=32"`
	Entries    []uint64 `mp4:"3,len=dynamic,size=64"`
}

func init() {
	mp4.AddAnyTypeBoxDef(&KidxBox{}, kidxBoxType)
}

// GetType / SetType satisfy mp4.IAnyType.
func (b *KidxBox) GetType() mp4.BoxType { return kidxBoxType }
func (b *KidxBox) SetType(mp4.BoxType)  {}

// GetFieldLength binds the dynamic Entries array to EntryCount, mirroring
// the library's built-in boxes such as Stss.
func (b *KidxBox) GetFieldLength(name string, ctx mp4.Context) uint {
	if name == "Entries" {
		return uint(b.EntryCount)
	}
	return b.BaseCustomFieldObject.GetFieldLength(name, ctx)
}

func packEntry(e Entry) uint64 { return uint64(e.Sample)<<32 | uint64(e.CRC) }

func unpackEntry(v uint64) Entry {
	return Entry{Sample: uint32(v >> 32), CRC: uint32(v)}
}

// entries converts between box words and the public Entry type.
func (b *KidxBox) entries() []Entry {
	out := make([]Entry, b.EntryCount)
	for i, v := range b.Entries {
		out[i] = unpackEntry(v)
	}
	return out
}

func boxFromEntries(entries []Entry) *KidxBox {
	b := &KidxBox{Magic: kidxMagic, Version: kidxVersion, EntryCount: uint32(len(entries))}
	b.Entries = make([]uint64, len(entries))
	for i, e := range entries {
		b.Entries[i] = packEntry(e)
	}
	return b
}

// validateShape enforces structural invariants reflection cannot express.
func (b *KidxBox) validateShape(payloadSize uint64) error {
	if b.Magic != kidxMagic {
		return clock.E(clock.PhaseIndex, 0, -1, false, "kidx magic mismatch", nil)
	}
	if b.Version != kidxVersion {
		return clock.E(clock.PhaseIndex, 0, -1, false, "unsupported kidx version", nil)
	}
	if uint32(len(b.Entries)) != b.EntryCount {
		return clock.E(clock.PhaseIndex, 0, -1, false,
			"kidx entry count does not match payload length", nil)
	}
	want := uint64(kidxHeader+4) + uint64(b.EntryCount)*kidxEntry
	if payloadSize != 0 && want != payloadSize {
		return clock.E(clock.PhaseIndex, 0, -1, false,
			"kidx declared size disagrees with box length", nil)
	}
	return nil
}

// crcOf computes the per-entry checksum the same way writer and reader use.
func crcOf(sample uint32) uint32 {
	var nb [4]byte
	binary.BigEndian.PutUint32(nb[:], sample)
	return crc32.ChecksumIEEE(nb[:])
}

// NewEntry builds an index entry with the checksum filled in.
func NewEntry(sample uint32) Entry {
	return Entry{Sample: sample, CRC: crcOf(sample)}
}

// WriteMP4 serializes entries into a standalone MP4 file containing the kidx
// box, using go-mp4 for the box framing (StartBox/Marshal/EndBox).
func WriteMP4(w io.WriteSeeker, entries []Entry) error {
	box := boxFromEntries(entries)
	mw := mp4.NewWriter(w)
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: kidxBoxType}); err != nil {
		return clock.E(clock.PhaseIndex, 0, -1, false, "kidx box header write failed", err)
	}
	if _, err := mp4.Marshal(mw, box, mp4.Context{}); err != nil {
		return clock.E(clock.PhaseIndex, 0, -1, false, "kidx marshal failed", err)
	}
	if _, err := mw.EndBox(); err != nil {
		return clock.E(clock.PhaseIndex, 0, -1, false, "kidx box finalize failed", err)
	}
	return nil
}

// ReadMP4 parses a kidx-only MP4 file.
func ReadMP4(r io.ReadSeeker) ([]Entry, error) {
	var found []Entry
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type != kidxBoxType {
			return nil, nil
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, clock.E(clock.PhaseIndex, 0, -1, false, "kidx payload unreadable", err)
		}
		kidx, ok := box.(*KidxBox)
		if !ok {
			return nil, clock.E(clock.PhaseIndex, 0, -1, false, "kidx payload wrong type", nil)
		}
		// Payload size excludes the common 8-byte box header.
		payloadSize := h.BoxInfo.Size - h.BoxInfo.HeaderSize
		if err := kidx.validateShape(payloadSize); err != nil {
			return nil, err
		}
		found = kidx.entries()
		return nil, nil
	})
	if err != nil {
		if pe, ok := clock.AsError(err); ok {
			return nil, pe
		}
		return nil, clock.E(clock.PhaseIndex, 0, -1, false, "kidx read failed", err)
	}
	if found == nil {
		return nil, clock.E(clock.PhaseIndex, 0, -1, false, "kidx box missing", nil)
	}
	return found, nil
}

// Validate checks every entry's checksum and verifies it points at a complete
// keyframe sample. Damaged indexes are rejected as a whole; the function never
// repairs or drops bad entries silently.
func Validate(c codec.Codec, track uint32, entries []Entry, completeKeySamples map[uint32]bool) error {
	switch c {
	case codec.H264, codec.H265, codec.AV1:
	default:
		return clock.E(clock.PhaseIndex, track, -1, false,
			"unsupported codec for index validation: "+string(c), nil)
	}
	seen := map[uint32]bool{}
	var last uint32
	for i, e := range entries {
		if e.Sample == 0 {
			return clock.E(clock.PhaseIndex, track, int(e.Sample), false,
				"index entry has zero sample number", nil)
		}
		if crcOf(e.Sample) != e.CRC {
			return clock.E(clock.PhaseIndex, track, int(e.Sample), false,
				"index entry checksum mismatch; refusing to repair", nil)
		}
		if i > 0 && e.Sample <= last {
			return clock.E(clock.PhaseIndex, track, int(e.Sample), false,
				"index entries must be strictly increasing", nil)
		}
		if seen[e.Sample] {
			return clock.E(clock.PhaseIndex, track, int(e.Sample), false,
				"duplicate index entry", nil)
		}
		seen[e.Sample] = true
		if !completeKeySamples[e.Sample] {
			return clock.E(clock.PhaseIndex, track, int(e.Sample), false,
				"index points at a sample that is not a complete keyframe", nil)
		}
		last = e.Sample
	}
	return nil
}
