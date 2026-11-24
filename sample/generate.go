// Package sample generates deterministic local RTP, audio and MP4 fixtures.
package sample

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"example.com/media-kernel/codec"
	"example.com/media-kernel/index"
	"example.com/media-kernel/ingest"
)

// Fixture is the complete self-generated verification input.
type Fixture struct {
	Name               string
	RawRTP             [][]byte
	NormalMP4          []byte
	CorruptMP4         []byte
	InputHash          string
	ExpectedMissing    []uint64
	ExpectedDuplicates int
}

type audioSpec struct {
	config  int
	payload []byte
	samples int64
	sent    bool
}

// Generate creates normal packets plus duplicate, wrap, reorder, truncation and
// missing-packet cases. All bytes are deterministic and generated locally.
func Generate() (*Fixture, error) {
	specs := []audioSpec{
		{config: 0, payload: codec.BuildOpusPacket(0, false, 16), samples: 480, sent: true},
		{config: 1, payload: codec.BuildOpusPacket(1, true, 20), samples: 960, sent: true},
		{config: 16, payload: codec.BuildOpusPacket(16, false, 8), samples: 120, sent: true},
		{config: 19, payload: codec.BuildOpusPacket(19, true, 24), samples: 960, sent: true},
		// Code 2 with a first-frame length far beyond the two-byte packet.
		{config: 0, payload: []byte{0x02, 200}, samples: 480, sent: true},
		{config: 0, payload: codec.BuildOpusPacket(0, false, 16), samples: 480, sent: true},
		{config: 3, payload: codec.BuildOpusPacket(3, true, 32), samples: 2880, sent: false},
		{config: 1, payload: codec.BuildOpusPacket(1, false, 20), samples: 960, sent: true},
	}

	const startSeq = uint16(65532)
	timestamp := uint32(0)
	byLogical := make(map[int][]byte, len(specs))
	for i, spec := range specs {
		seq := startSeq + uint16(i) // wraps through 65535 to 0,1,2,3
		raw, err := ingest.BuildRTP(seq, timestamp, spec.payload, i == 0 || i == len(specs)-1)
		if err != nil {
			return nil, err
		}
		byLogical[i] = raw
		timestamp += uint32(spec.samples)
	}

	// Arrival order: normal, duplicate #1, reorder #3 before #2, then the rest.
	order := []int{0, 1, 1, 3, 2, 4, 5, 7}
	raw := make([][]byte, 0, len(order))
	for _, i := range order {
		raw = append(raw, byLogical[i])
	}

	normalMP4 := index.BuildMP4Fixture(index.MP4FixtureConfig{
		SampleCount: 10,
		SyncSamples: []uint32{1, 4, 7, 10},
	})
	corruptMP4 := index.BuildMP4Fixture(index.MP4FixtureConfig{
		SampleCount: 8,
		SyncSamples: []uint32{1, 4, 7},
		Corrupt:     map[uint32]bool{4: true},
	})

	f := &Fixture{
		Name:               "self-generated-multicam-fixture",
		RawRTP:             raw,
		NormalMP4:          normalMP4,
		CorruptMP4:         corruptMP4,
		ExpectedMissing:    []uint64{65538},
		ExpectedDuplicates: 1,
	}
	f.InputHash = hashInput(f)
	return f, nil
}

func hashInput(f *Fixture) string {
	h := sha256.New()
	write := func(b []byte) {
		var l [8]byte
		binary.LittleEndian.PutUint64(l[:], uint64(len(b)))
		h.Write(l[:])
		h.Write(b)
	}
	write([]byte(f.Name))
	for _, raw := range f.RawRTP {
		write(raw)
	}
	write(f.NormalMP4)
	write(f.CorruptMP4)
	return hex.EncodeToString(h.Sum(nil))
}
