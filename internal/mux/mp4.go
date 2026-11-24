// Package mux writes the random-access index into an MP4 container using
// github.com/Eyevinn/mp4ff. The index lives in a custom "mdir/midx" box
// hierarchy inside moov, so the output is a standards-shaped MP4 file whose
// byte layout can be verified by re-parsing with mp4ff itself.
package mux

import (
	"bytes"
	"encoding/binary"
	"io"
	"unsafe"

	"github.com/Eyevinn/mp4ff/mp4"

	"mediasync/internal/clock"
	"mediasync/internal/index"
)

// magic values identify the custom index boxes.
const (
	BoxMediaDir  = "mdir"
	BoxMediaIdx  = "midx"
	indexVersion = 1
)

// indexRecordWire is the fixed on-disk layout of one index entry. All
// integers are big-endian as required by the ISO BMFF convention. Three
// int64 fields plus two uint32 fields give 32 bytes exactly with no padding.
type indexRecordWire struct {
	TimeTicks  int64
	ByteOffset int64
	FrameIndex int64
	Length     uint32
	Flags      uint32 // bit0 = concealed
}

const wireRecordSize = 32

// Guard against accidental struct padding changing the on-disk record size.
var _ [wireRecordSize]byte = [unsafe.Sizeof(indexRecordWire{})]byte{}

// WriteIndexMP4 serializes records into an MP4 byte stream:
// ftyp, moov[mdir[midx(payload)]]. The payload starts with a small header
// (version, count, input length) followed by fixed-size records.
func WriteIndexMP4(records []index.Record, inputLength int64) ([]byte, error) {
	if int64(len(records)) > index.Limit {
		return nil, clock.ErrorAt(clock.ErrLimitExceeded, 0,
			"index exceeds %d records", index.Limit)
	}
	payload, err := marshalIndexPayload(records, inputLength)
	if err != nil {
		return nil, err
	}

	f := mp4.NewFile()
	ftyp := mp4.NewFtyp("msyn", 0, []string{"msyn", "iso6"})
	moov := newIndexMoov(payload)
	f.Children = append(f.Children, ftyp, moov)
	f.Ftyp = ftyp
	f.Moov = moov

	var buf bytes.Buffer
	if err := f.Encode(&buf); err != nil {
		return nil, clock.AsMediaError(err)
	}
	return buf.Bytes(), nil
}

// newIndexMoov builds a complete minimal trak tree so mp4ff can re-parse
// the file without touching nil nested boxes, then attaches the custom
// mdir/midx index boxes as moov children.
func newIndexMoov(payload []byte) *mp4.MoovBox {
	moov := mp4.NewMoovBox()
	moov.AddChild(mp4.CreateMvhd())

	trak := mp4.NewTrakBox()
	trak.AddChild(mp4.CreateTkhd())
	mdia := mp4.NewMdiaBox()
	mdia.AddChild(&mp4.MdhdBox{})
	if hdlr, err := mp4.CreateHdlr("soun"); err == nil {
		mdia.AddChild(hdlr)
	}
	minf := mp4.NewMinfBox()
	minf.AddChild(mp4.CreateSmhd())
	dinf := mp4.NewGenericContainerBox("dinf")
	dinf.AddChild(mp4.CreateDref())
	minf.AddChild(dinf)
	stbl := mp4.NewStblBox()
	// A zero-length but present stts keeps SampleCount non-nil, which is
	// what mp4ff's fragmentation probe dereferences on decode.
	stbl.AddChild(&mp4.SttsBox{SampleCount: []uint32{}, SampleTimeDelta: []uint32{}})
	stbl.AddChild(&mp4.StscBox{})
	stbl.AddChild(&mp4.StszBox{})
	stbl.AddChild(&mp4.StcoBox{})
	minf.AddChild(stbl)
	mdia.AddChild(minf)
	trak.AddChild(mdia)
	moov.AddChild(trak)

	mdir := mp4.NewGenericContainerBox(BoxMediaDir)
	midx := mp4.CreateUnknownBox(BoxMediaIdx, uint64(8+len(payload)), payload)
	mdir.AddChild(midx)
	moov.AddChild(mdir)
	return moov
}

func marshalIndexPayload(records []index.Record, inputLength int64) ([]byte, error) {
	// 4 version + 4 count + 8 input length = 16 byte header.
	header := make([]byte, 16)
	binary.BigEndian.PutUint32(header[0:4], indexVersion)
	binary.BigEndian.PutUint32(header[4:8], uint32(len(records)))
	binary.BigEndian.PutUint64(header[8:16], uint64(inputLength))

	out := bytes.NewBuffer(header)
	for _, rec := range records {
		if rec.ByteOffset+rec.Length > inputLength {
			return nil, clock.ErrorAt(clock.ErrOverflow, rec.ByteOffset,
				"record %d escapes input length", rec.ID)
		}
		wire := indexRecordWire{
			TimeTicks:  int64(rec.TimeTicks),
			ByteOffset: rec.ByteOffset,
			FrameIndex: rec.FrameIndex,
			Length:     uint32(rec.Length),
		}
		if rec.Concealed {
			wire.Flags |= 1
		}
		if err := binary.Write(out, binary.BigEndian, &wire); err != nil {
			return nil, clock.AsMediaError(err)
		}
	}
	return out.Bytes(), nil
}

// ReadIndexMP4 re-parses an MP4 byte stream produced by WriteIndexMP4 and
// extracts the index records, proving the container is random-access
// readable and the records survived the round trip.
func ReadIndexMP4(data []byte) (records []index.Record, inputLength int64, err error) {
	f, derr := mp4.DecodeFile(bytes.NewReader(data))
	if derr != nil {
		return nil, 0, clock.AsMediaError(derr)
	}
	if f.Moov == nil {
		return nil, 0, clock.NewError(clock.ErrCorrupt, "moov box missing")
	}
	payload, perr := findMidx(f.Moov)
	if perr != nil {
		return nil, 0, perr
	}
	return unmarshalIndexPayload(payload)
}

// findMidx walks moov children for mdir -> midx and returns its payload.
// On decode mp4ff does not know our custom box types, so mdir comes back as
// an UnknownBox whose payload is itself a stream of child boxes.
func findMidx(moov *mp4.MoovBox) ([]byte, error) {
	for _, child := range moov.GetChildren() {
		if child.Type() != BoxMediaDir {
			continue
		}
		// Encoded path: mdir was created as a generic container.
		if container, ok := child.(mp4.ContainerBox); ok {
			for _, sub := range container.GetChildren() {
				if sub.Type() == BoxMediaIdx {
					if ub, ok := sub.(*mp4.UnknownBox); ok {
						return ub.Payload(), nil
					}
				}
			}
		}
		// Decoded path: mdir is an UnknownBox holding nested box bytes.
		if ub, ok := child.(*mp4.UnknownBox); ok {
			if payload, err := extractMidxFromUnknown(ub.Payload()); err == nil {
				return payload, nil
			}
		}
	}
	return nil, clock.NewError(clock.ErrCorrupt, "midx box not found in moov/mdir")
}

// extractMidxFromUnknown parses the nested boxes inside an mdir payload and
// returns the midx payload.
func extractMidxFromUnknown(mdirPayload []byte) ([]byte, error) {
	r := bytes.NewReader(mdirPayload)
	for r.Len() > 0 {
		hdr, err := mp4.DecodeHeader(r)
		if err != nil {
			return nil, clock.AsMediaError(err)
		}
		headerLen := hdr.Hdrlen
		bodyLen := int(hdr.Size) - headerLen
		if bodyLen < 0 || int64(bodyLen) > int64(r.Len()) {
			return nil, clock.NewError(clock.ErrCorrupt, "malformed nested box size")
		}
		body := make([]byte, bodyLen)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, clock.AsMediaError(err)
		}
		if hdr.Name == BoxMediaIdx {
			return body, nil
		}
	}
	return nil, clock.NewError(clock.ErrCorrupt, "midx box absent from mdir payload")
}

func unmarshalIndexPayload(payload []byte) ([]index.Record, int64, error) {
	if len(payload) < 16 {
		return nil, 0, clock.TruncationError(int64(len(payload)), int64(16-len(payload)), "midx header")
	}
	if v := binary.BigEndian.Uint32(payload[0:4]); v != indexVersion {
		return nil, 0, clock.NewError(clock.ErrCorrupt, "unsupported midx version %d", v)
	}
	count := binary.BigEndian.Uint32(payload[4:8])
	inputLength := int64(binary.BigEndian.Uint64(payload[8:16]))
	body := payload[16:]
	if len(body) != int(count)*wireRecordSize {
		missing := int(count)*wireRecordSize - len(body)
		if missing < 0 {
			return nil, 0, clock.NewError(clock.ErrCorrupt, "midx payload has %d trailing bytes", -missing)
		}
		return nil, 0, clock.TruncationError(int64(len(payload)), int64(missing), "midx records")
	}
	recs := make([]index.Record, 0, count)
	r := bytes.NewReader(body)
	for i := uint32(0); i < count; i++ {
		var wire indexRecordWire
		if err := binary.Read(r, binary.BigEndian, &wire); err != nil {
			return nil, 0, clock.AsMediaError(err)
		}
		recs = append(recs, index.Record{
			ID:         int64(i),
			TimeTicks:  clock.Tick(wire.TimeTicks),
			TimeMs:     clock.Tick(wire.TimeTicks).Milli(),
			ByteOffset: wire.ByteOffset,
			FrameIndex: wire.FrameIndex,
			Length:     int64(wire.Length),
			Concealed:  wire.Flags&1 != 0,
		})
	}
	return recs, inputLength, nil
}
