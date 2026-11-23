package fixture

import (
	"errors"
	"fmt"
)

// Generate renders one spec into a complete MP4 byte stream. The layout is
// ftyp -> moov -> mdat; every size field is patched after the fact, which
// also lets corruption variants produce deliberately inconsistent boxes.
func Generate(s Spec) ([]byte, error) {
	if len(s.Tracks) == 0 {
		return nil, errors.New("fixture spec has no tracks")
	}
	layouts := make([]trackLayout, len(s.Tracks))
	payloadLen := 0
	for i, ts := range s.Tracks {
		layouts[i] = planTables(s, ts)
		for _, sz := range layouts[i].sizes {
			payloadLen += int(sz)
		}
	}

	var pre builder
	pre.writeFtyp()
	// moov size has to be measured before mdat offsets are known.
	var moovOnly builder
	moovOnly.writeMoov(s, layouts, 0)
	moovSize := len(moovOnly.buf)
	mdatHeader := 8
	mdatOffset := uint32(len(pre.buf) + moovSize + mdatHeader)

	// Assign each track a distinct base offset inside mdat so tracks never
	// alias the same payload bytes.
	payloadCursor := uint64(mdatOffset)
	for i := range layouts {
		layouts[i].mdatBase = payloadCursor
		var trackBytes uint64
		for _, sz := range layouts[i].sizes {
			trackBytes += uint64(sz)
		}
		payloadCursor += trackBytes
	}

	var b builder
	b.writeFtyp()
	b.writeMoov(s, layouts, mdatOffset)
	b.writeMdat(layouts, s.Seed)

	out := b.buf
	switch s.Corruption {
	case CorruptTruncatedMdat:
		// Cut the file inside mdat but keep the declared mdat size, so the
		// structural scan sees a box running past EOF.
		cut := len(out) - payloadLen/3
		if cut <= int(mdatOffset) {
			cut = int(mdatOffset) + 1
		}
		out = out[:cut]
	case CorruptBackwardTimestamps:
		// Damage already encoded in stts; nothing else to patch.
	}
	return out, nil
}

func (b *builder) writeFtyp() {
	closeBox := b.box("ftyp")
	b.fourcc("isom")
	b.u32(0x200)
	b.fourcc("isom")
	b.fourcc("iso2")
	b.fourcc("mp41")
	closeBox()
}

func (b *builder) writeMdat(layouts []trackLayout, seed int64) {
	closeBox := b.box("mdat")
	for _, l := range layouts {
		for i := 0; i < len(l.sizes); i++ {
			b.raw(payloadBytes(seed, l.spec.ID, i, int(l.sizes[i])))
		}
	}
	closeBox()
}

// MustGenerate panics on error; intended for package-level test fixtures.
func MustGenerate(s Spec) []byte {
	out, err := Generate(s)
	if err != nil {
		panic(fmt.Sprintf("fixture %s: %v", s.Name, err))
	}
	return out
}

// WriteSet renders every spec in the standard set keyed by name.
func WriteSet(seed int64) (map[string][]byte, error) {
	out := make(map[string][]byte)
	for _, spec := range StandardSet(seed) {
		raw, err := Generate(spec)
		if err != nil {
			return nil, fmt.Errorf("spec %s: %w", spec.Name, err)
		}
		out[spec.Name] = raw
	}
	return out, nil
}
