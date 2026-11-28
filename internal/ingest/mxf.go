package ingest

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"

	"mediapack/internal/fixture"
	"mediapack/internal/model"
)

// mxfReader walks one partition's KLV stream.
type mxfReader struct {
	file string
	data []byte
	pos  int
	out  *Container
}

// ParseMXF parses a project-subset MXF file. Items are read at partition and
// KLV boundaries; truncated items and out-of-range declared lengths fail with
// the exact file offset and a KLV field path.
func ParseMXF(path string) (*Container, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.WrapField(model.KindInvalidInput, path, -1, "<file>",
			"cannot open input", err)
	}
	defer f.Close()
	return ParseMXFReader(f, path)
}

// ParseMXFReader parses an MXF stream under a logical file name. Buffering the
// stream first keeps corrupt inputs out of the output directory.
func ParseMXFReader(r io.Reader, name string) (*Container, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, model.WrapField(model.KindInvalidInput, name, -1, "<file>",
			"cannot read input", err)
	}
	mr := &mxfReader{
		file: name,
		data: data,
		out:  &Container{Format: "mxf", FileName: name},
	}
	if err := mr.parse(); err != nil {
		return nil, err
	}
	for _, t := range mr.out.Tracks {
		finalizeTrack(t, mr.out)
	}
	return mr.out, nil
}

// parse reads the header partition pack then every track item group.
func (r *mxfReader) parse() error {
	key, value, itemOff, err := r.readKLV()
	if err != nil {
		return err
	}
	pk := fixture.PartitionKey()
	if !bytes.Equal(key[:], pk[:]) {
		return model.NewFieldError(model.KindInvalidInput, r.file, int64(itemOff),
			"partition/header", "first KLV item is not a header partition pack")
	}
	if len(value) < 4 {
		return model.Truncated(r.file, int64(itemOff), "partition/payload",
			"partition payload shorter than 4 bytes")
	}
	var cur *Track
	trackNo := -1
	for r.pos < len(r.data) {
		key, value, off, err := r.readKLV()
		if err != nil {
			return err
		}
		path := mxfItemPath(key)
		if key[15] == fixture.IDTrackStart() {
			trackNo++
			cur = &Track{
				Index:   trackNo,
				Sources: map[string]model.SourceRef{},
			}
			r.out.Tracks = append(r.out.Tracks, cur)
			if len(value) < 4 {
				return model.Truncated(r.file, int64(off), path, "track id value truncated")
			}
			cur.TrackID = binary.BigEndian.Uint32(value)
			cur.Sources["track_id"] = model.SourceOnly(r.file, path, int64(off))
			continue
		}
		if cur == nil {
			return model.NewFieldError(model.KindInvalidInput, r.file, int64(off), path,
				"item appears before any track start")
		}
		if err := r.applyItem(cur, key[15], value, off, path); err != nil {
			return err
		}
	}
	return nil
}

// readKLV reads one key/length/value triple, enforcing every boundary.
func (r *mxfReader) readKLV() (key [16]byte, value []byte, offset int, err error) {
	offset = r.pos
	if len(r.data)-r.pos < 16 {
		return key, nil, offset, model.Truncated(r.file, int64(offset),
			"partition/KLV/key", "KLV key is truncated")
	}
	copy(key[:], r.data[r.pos:r.pos+16])
	r.pos += 16
	length, n, lerr := readBERLength(r.data[r.pos:])
	if lerr != nil {
		return key, nil, offset, model.Truncated(r.file, int64(r.pos),
			"partition/KLV/length", lerr.Error())
	}
	r.pos += n
	if length > len(r.data)-r.pos {
		return key, nil, offset, model.OutOfRange(r.file, int64(offset),
			"partition/KLV/length",
			"declared KLV length crosses partition/file boundary")
	}
	value = r.data[r.pos : r.pos+length]
	r.pos += length
	return key, value, offset, nil
}

// readBERLength decodes an ASN.1 BER length. It returns the length and the
// number of header bytes consumed.
func readBERLength(buf []byte) (int, int, error) {
	if len(buf) == 0 {
		return 0, 0, io.ErrUnexpectedEOF
	}
	first := buf[0]
	if first < 0x80 {
		return int(first), 1, nil
	}
	n := int(first & 0x7f)
	if n == 0 || n > 8 {
		return 0, 0, errBadBERLength()
	}
	if len(buf) < 1+n {
		return 0, 0, io.ErrUnexpectedEOF
	}
	var v int
	for i := 0; i < n; i++ {
		v = v<<8 | int(buf[1+i])
	}
	if v < 0 {
		return 0, 0, errBadBERLength()
	}
	return v, 1 + n, nil
}

func errBadBERLength() error {
	return model.NewFieldError(model.KindOutOfRange, "", -1,
		"partition/KLV/length", "unsupported BER length encoding")
}

func mxfItemPath(key [16]byte) string {
	return "partition/track/KLV[0x" + hexByte(key[15]) + "]"
}

func hexByte(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&0x0f]})
}
