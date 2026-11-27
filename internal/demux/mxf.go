// Package demux turns the generated MXF KLV subset and ISO-BMFF MP4 files
// into container-neutral tracks plus provenance-annotated samples. No sample
// bytes are decoded or altered.
package demux

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"mediapack/internal/binio"
	"mediapack/internal/klv"
	"mediapack/internal/model"
)

// Container is the demuxed view of one input file.
type Container struct {
	Path    string
	Format  string // "mxf" | "mp4"
	Tracks  []model.TrackMeta
	Samples map[uint32][]model.Sample
}

// MXF opens and parses the minimal MXF KLV image produced by fixture.
func MXF(path string, r io.ReaderAt, size int64) (*Container, error) {
	br := &readerAtSeeker{r: r, size: size}
	c := &Container{Path: path, Format: "mxf", Samples: map[uint32][]model.Sample{}}
	var pending *mxfRow // table KLV awaiting the immediately following essence

	for br.pos < size {
		runStart := br.pos
		var key [16]byte
		if err := br.readFull(key[:]); err != nil {
			return nil, wrap(err, path, runStart)
		}
		length, hdr, err := binio.ReadBERLen(br)
		if err != nil {
			return nil, wrap(err, path, br.pos)
		}
		valueStart := runStart + 16 + int64(hdr)
		if length > uint64(size-valueStart) {
			return nil, model.NewError(model.CodeTruncatedBox,
				"KLV value length exceeds file").WithFile(path).WithOffset(runStart)
		}
		value, err := br.readN(int64(length))
		if err != nil {
			return nil, wrap(err, path, runStart)
		}
		switch key {
		case klv.KeyHeaderPack:
			if len(value) < 20 || string(value[:16]) != klv.HeaderMagic {
				return nil, model.NewError(model.CodeMalformedContainer,
					"bad MXF header pack magic").WithFile(path).WithOffset(runStart)
			}
		case klv.KeyPreface:
			meta, err := parsePreface(value, path, runStart)
			if err != nil {
				return nil, err
			}
			c.Tracks = append(c.Tracks, *meta)
		case klv.KeyTrack:
			row, err := parseTrackRow(value, path, runStart)
			if err != nil {
				return nil, err
			}
			pending = row
		case klv.KeyEssence:
			if pending == nil {
				return nil, model.NewError(model.CodeMalformedContainer,
					"essence without preceding index table").
					WithFile(path).WithOffset(runStart)
			}
			if err := attachEssence(c, r, pending, value, valueStart, path); err != nil {
				return nil, err
			}
			pending = nil
		default:
			return nil, model.NewError(model.CodeMalformedContainer,
				fmt.Sprintf("unknown KLV key %x", key[12:])).
				WithFile(path).WithOffset(runStart)
		}
	}
	if pending != nil {
		return nil, model.NewError(model.CodeTruncatedBox,
			"index table without essence").WithFile(path).WithTrack(pending.trackID)
	}
	if err := c.validate(path); err != nil {
		return nil, err
	}
	return c, nil
}

// mxfRow is one single-row index table KLV (40-byte value).
type mxfRow struct {
	trackID  uint32
	index    uint32
	dts      uint64
	duration uint64
	offset   uint64
	size     uint32
	rtpRaw   uint32
}

func parsePreface(v []byte, path string, at int64) (*model.TrackMeta, error) {
	rd := bytes.NewReader(v)
	var ts, tid, rot, cl, ct, cr, cb, rtpBase, rtpStep uint32
	readU32(rd, &ts)
	readU32(rd, &tid)
	codecLen, _ := rd.ReadByte()
	codecStr := string(readBytes(rd, int(codecLen)))
	hl, _ := rd.ReadByte()
	handler := string(readBytes(rd, int(hl)))
	readU32(rd, &rot)
	readU32(rd, &cl)
	readU32(rd, &ct)
	readU32(rd, &cr)
	readU32(rd, &cb)
	var prim, trc, mx uint16
	readU16(rd, &prim)
	readU16(rd, &trc)
	readU16(rd, &mx)
	flag, _ := rd.ReadByte()
	readU32(rd, &rtpBase)
	readU32(rd, &rtpStep)
	_ = rtpBase // RTP base/step inform timeline expansion, not TrackMeta
	_ = rtpStep
	if ts == 0 {
		return nil, model.NewError(model.CodeMalformedContainer,
			"invalid preface: zero timescale").WithFile(path).WithOffset(at)
	}
	if rot%90 != 0 {
		return nil, model.NewError(model.CodeMalformedContainer,
			fmt.Sprintf("rotation %d is not a multiple of 90", rot)).
			WithFile(path).WithOffset(at).WithTrack(tid)
	}
	return &model.TrackMeta{
		TrackID:   tid,
		Timescale: ts,
		Codec:     codecStr,
		Handler:   handler,
		Rotation:  model.Field[model.Rotation]{Value: model.Rotation{Degrees: int32(rot)}, Source: model.SourceMXF, Raw: fmt.Sprintf("%d", rot)},
		Crop: model.Field[model.Crop]{Value: model.Crop{Left: cl, Top: ct, Right: cr, Bottom: cb},
			Source: model.SourceMXF, Raw: fmt.Sprintf("%d,%d,%d,%d", cl, ct, cr, cb)},
		Color: model.Field[model.Color]{
			Value:  model.Color{Primaries: prim, Transfer: trc, Matrix: mx, FullRange: flag == 1},
			Source: model.SourceMXF,
			Raw:    fmt.Sprintf("%d/%d/%d fr=%d", prim, trc, mx, flag),
		},
	}, nil
}

func parseTrackRow(v []byte, path string, at int64) (*mxfRow, error) {
	if len(v) != 40 {
		return nil, model.NewError(model.CodeMalformedContainer,
			fmt.Sprintf("index row must be 40 bytes, got %d", len(v))).
			WithFile(path).WithOffset(at)
	}
	return &mxfRow{
		trackID:  binary.BigEndian.Uint32(v[0:4]),
		index:    binary.BigEndian.Uint32(v[4:8]),
		dts:      binary.BigEndian.Uint64(v[8:16]),
		duration: binary.BigEndian.Uint64(v[16:24]),
		offset:   binary.BigEndian.Uint64(v[24:32]),
		size:     binary.BigEndian.Uint32(v[32:36]),
		rtpRaw:   binary.BigEndian.Uint32(v[36:40]),
	}, nil
}

func attachEssence(c *Container, src io.ReaderAt, row *mxfRow,
	value []byte, valueStart int64, path string,
) error {
	if c.meta(row.trackID).Timescale == 0 {
		return model.NewError(model.CodeMissingTrack,
			"index references track without preface").
			WithFile(path).WithTrack(row.trackID).WithOffset(valueStart)
	}
	if int64(row.offset) != valueStart {
		return model.NewError(model.CodeOutOfRangeSample,
			"declared sample offset disagrees with essence position").
			WithFile(path).WithTrack(row.trackID).WithOffset(int64(row.offset))
	}
	if int64(row.size) != int64(len(value)) {
		return model.NewError(model.CodeOutOfRangeSample,
			"sample size disagrees with KLV value length").
			WithFile(path).WithTrack(row.trackID).WithOffset(valueStart)
	}
	payload := make([]byte, row.size)
	if _, err := src.ReadAt(payload, valueStart); err != nil {
		return model.NewError(model.CodeTruncatedBox, "cannot read sample payload").
			WithFile(path).WithTrack(row.trackID).WithOffset(valueStart)
	}
	meta := c.meta(row.trackID)
	_, dtsTick := model.ScaleToMaster(int64(row.dts), meta.Timescale)
	c.Samples[row.trackID] = append(c.Samples[row.trackID], model.Sample{
		Source:      path,
		SourceTrack: row.trackID,
		Index:       row.index,
		DTS:         dtsTick,
		Duration:    model.DeltaMaster(row.duration, meta.Timescale),
		SrcOffset:   valueStart,
		Size:        row.size,
		RTPRaw:      row.rtpRaw,
		Payload:     payload,
	})
	return nil
}

func (c *Container) meta(tid uint32) model.TrackMeta {
	for i := range c.Tracks {
		if c.Tracks[i].TrackID == tid {
			return c.Tracks[i]
		}
	}
	return model.TrackMeta{TrackID: tid, Timescale: 0}
}

func (c *Container) validate(path string) error {
	seen := map[uint32]bool{}
	for _, t := range c.Tracks {
		if seen[t.TrackID] {
			return model.NewError(model.CodeDuplicateTarget,
				"duplicate MXF track id").WithFile(path).WithTrack(t.TrackID)
		}
		seen[t.TrackID] = true
		if len(c.Samples[t.TrackID]) == 0 {
			return model.NewError(model.CodeMalformedContainer,
				"track declared without essence").WithFile(path).WithTrack(t.TrackID)
		}
	}
	return nil
}

func wrap(err error, path string, off int64) error {
	if me, ok := model.AsMediaError(err); ok {
		if me.File == "" {
			me.File = path
		}
		if me.Offset < 0 {
			me.Offset = off
		}
		return me
	}
	if errors.Is(err, io.EOF) {
		return model.NewError(model.CodeTruncatedBox, "unexpected EOF").
			WithFile(path).WithOffset(off)
	}
	return model.NewError(model.CodeTruncatedBox, err.Error()).WithFile(path).WithOffset(off)
}

func readU16(r *bytes.Reader, v *uint16) {
	var b [2]byte
	_, _ = io.ReadFull(r, b[:])
	*v = binary.BigEndian.Uint16(b[:])
}
func readU32(r *bytes.Reader, v *uint32) {
	var b [4]byte
	_, _ = io.ReadFull(r, b[:])
	*v = binary.BigEndian.Uint32(b[:])
}
func readBytes(r *bytes.Reader, n int) []byte {
	b := make([]byte, n)
	_, _ = io.ReadFull(r, b)
	return b
}
