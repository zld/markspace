package container

import (
	"encoding/binary"
	"fmt"

	"archivist/internal/model"
)

// Generate builds a complete MP4/MOV byte stream from the spec. All sample
// payloads derive from the seed, so output is deterministic.
func Generate(spec FixtureSpec) (*Fixture, error) {
	if len(spec.Tracks) > model.MaxTracks {
		return nil, model.AtTrack(model.StageFixture, model.ErrLimitExceeded, 0,
			fmt.Sprintf("tracks %d > %d", len(spec.Tracks), model.MaxTracks))
	}
	var total uint32
	for _, t := range spec.Tracks {
		total += t.SampleCount
	}
	if total > model.MaxSamples {
		return nil, model.AtTrack(model.StageFixture, model.ErrLimitExceeded, 0,
			fmt.Sprintf("samples %d > %d", total, model.MaxSamples))
	}
	g := newGenerator(spec)
	g.buildPayloads()
	if err := g.buildBoxes(); err != nil {
		return nil, err
	}
	g.applyCorruption()
	return &Fixture{
		Name:                spec.Name,
		Data:                g.out.Bytes(),
		Spec:                spec,
		MdatOffset:          g.mdatOffset,
		MdatPayload:         g.mdatPayload,
		ExpectedSampleSizes: g.sizes,
		Digest:              g.digest(),
	}, nil
}

func (g *generator) buildBoxes() error {
	brand := []byte(g.spec.Brand)
	if len(brand) != 4 {
		brand = []byte("isom")
	}
	ftyp := make([]byte, 0, 16)
	ftyp = append(ftyp, brand...)
	ftyp = PutU32(ftyp, 0x200)
	ftyp = append(ftyp, brand...)
	ftyp = append(ftyp, []byte("iso2")...)
	ftyp = append(ftyp, []byte("mp41")...)
	g.out.WriteBox(BoxFtyp, ftyp)

	// mdat payload is known up front; render moov once to measure its length,
	// then render it for real so stco stores correct absolute offsets.
	for _, ts := range g.spec.Tracks {
		g.mdat.buf.Write(g.payloads[ts.ID])
	}
	g.mdatPayload = append([]byte(nil), g.mdat.Bytes()...)
	g.mdatOffset = uint64(g.out.Len()) + g.moovSize()
	g.out.WriteContainer(BoxMoov, func(c *Writer) {
		g.writeMvhd(c)
		for _, ts := range g.spec.Tracks {
			g.writeTrak(c, ts)
		}
	})
	g.out.WriteBox(BoxMdat, g.mdatPayload)
	return nil
}

func (g *generator) moovSize() uint64 {
	var probe Writer
	probe.WriteContainer(BoxMoov, func(c *Writer) {
		g.writeMvhd(c)
		for _, ts := range g.spec.Tracks {
			g.writeTrak(c, ts)
		}
	})
	return uint64(probe.Len())
}

func (g *generator) writeMvhd(w *Writer) {
	// ISO BMFF mvhd version 0 fixed body = 96 bytes (after fullbox prefix).
	body := make([]byte, 96)
	put := func(off int, v uint32) { binary.BigEndian.PutUint32(body[off:off+4], v) }
	var dur uint64
	for _, ts := range g.spec.Tracks {
		d := trackDuration(ts)
		mapped := d * uint64(g.spec.MovieTS) / uint64(ts.Timescale)
		if mapped > dur {
			dur = mapped
		}
	}
	put(12, g.spec.MovieTS)
	put(16, uint32(dur))
	binary.BigEndian.PutUint16(body[20:22], 0x0100) // rate 1.0
	body[24] = 0x01                                  // volume 1.0
	matrix := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	for i, m := range matrix {
		put(36+i*4, m)
	}
	put(92, uint32(len(g.spec.Tracks))+1) // next_track_ID
	w.WriteBox(BoxMvhd, FullBox(0, 0, body))
}
