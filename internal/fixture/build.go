package fixture

import (
	"bytes"
)

// BuiltFile is a generated container plus the byte offsets of its key boxes.
// Offsets point into the returned byte slice and remain valid after mutation
// helpers (which only rewrite existing bytes) run.
type BuiltFile struct {
	Data       []byte
	MdatOffset int // offset of the mdat box header
	MdatData   int // offset where sample bytes begin
	// TrackMdat gives, per track ID, the absolute offsets and sizes of samples.
	TrackMdat map[uint32][]SampleLocation
}

// SampleLocation pins one sample to real input bytes.
type SampleLocation struct {
	Offset int
	Size   int
	Sync   bool
}

// BuildFile renders a complete, index-consistent MP4 file from spec.
func BuildFile(spec MediaSpec) *BuiltFile {
	var moov bytes.Buffer
	moov.Write(mvhd())
	for _, t := range spec.Tracks {
		moov.Write(t.trak())
	}

	b := &Builder{}
	b.Write(ftyp(spec.Brand))
	b.Write(Box("moov", moov.Bytes()))

	mdatData := b.Size() + 8
	loc := make(map[uint32][]SampleLocation, len(spec.Tracks))
	var mdat bytes.Buffer
	for _, t := range spec.Tracks {
		for i := uint32(0); i < t.SampleCount; i++ {
			frame := SampleFrame{
				Index:   i,
				TrackID: t.TrackID,
				Payload: bytes.Repeat([]byte{byte(t.TrackID) + byte(i)}, t.PayloadSize),
			}
			raw := frame.Encode()
			loc[t.TrackID] = append(loc[t.TrackID], SampleLocation{
				Offset: mdatData + mdat.Len(),
				Size:   len(raw),
				Sync:   t.SyncInterval > 0 && i%t.SyncInterval == 0,
			})
			mdat.Write(raw)
		}
	}
	b.Write(Box("mdat", mdat.Bytes()))

	out := b.Bytes()
	fixupStco(out, loc)
	return &BuiltFile{
		Data:       append([]byte(nil), out...),
		MdatOffset: mdatData - 8,
		MdatData:   mdatData,
		TrackMdat:  loc,
	}
}

// fixupStco walks every stco box and fills its entries from sample locations
// in track order. Each generated sample is its own chunk.
func fixupStco(data []byte, loc map[uint32][]SampleLocation) {
	var order []uint32
	for id := range loc {
		order = append(order, id)
	}
	// Map iteration order is randomized; sort for deterministic patching.
	for i := 0; i < len(order); i++ {
		for j := i + 1; j < len(order); j++ {
			if order[j] < order[i] {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	var flat []uint32
	for _, id := range order {
		for _, s := range loc[id] {
			flat = append(flat, uint32(s.Offset))
		}
	}
	needle := []byte{'s', 't', 'c', 'o'}
	entry := 0
	for i := 0; i+8 <= len(data); i++ {
		if !bytes.Equal(data[i:i+4], needle) {
			continue
		}
		// stco full-box header: type at i, version/flags at i+4, count at i+8.
		countOff := i + 8
		count := int(binaryU32(data[countOff:]))
		first := countOff + 4
		for k := 0; k < count && entry < len(flat); k++ {
			binaryPutU32(data[first+k*4:], flat[entry])
			entry++
		}
	}
}

func binaryU32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
