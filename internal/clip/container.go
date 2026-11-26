package clip

import (
	"encoding/binary"
	"io"

	"github.com/abema/go-mp4"
)

// This package writes boxes explicitly: go-mp4's Marshal emits only the
// field payload of a box (never its 8-byte header), so every output box is
// framed here while the library remains responsible for field encoding and
// all reading/unmarshalling.

// leafWriter buffers a leaf box: a size placeholder header followed by the
// marshalled field payload. The real size is patched on Flush.
type leafWriter struct {
	w       io.WriteSeeker
	start   int64
	boxType string
}

func openLeaf(w io.WriteSeeker, boxType string) (*leafWriter, error) {
	start, err := w.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	hdr := make([]byte, 8)
	copy(hdr[4:8], boxType)
	if _, err := w.Write(hdr); err != nil {
		return nil, err
	}
	return &leafWriter{w: w, start: start, boxType: boxType}, nil
}

// marshalLeaf frames a go-mp4 leaf box: header placeholder, library field
// marshalling, then a patched size. FullBox headers (version/flags) are
// part of the marshalled payload because the library encodes them.
func marshalLeaf(w io.WriteSeeker, b mp4.IImmutableBox) error {
	lw, err := openLeaf(w, b.GetType().String())
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, b, mp4.Context{}); err != nil {
		return err
	}
	return lw.flush()
}

func (l *leafWriter) flush() error {
	end, err := l.w.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	size := uint32(end - l.start)
	if _, err := l.w.Seek(l.start, io.SeekStart); err != nil {
		return err
	}
	if err := binary.Write(l.w, binary.BigEndian, size); err != nil {
		return err
	}
	_, err = l.w.Seek(end, io.SeekStart)
	return err
}

// containerWriter emits a container box the same way; children are written
// between openContainer and close, which patches the final size.
type containerWriter struct {
	w       io.WriteSeeker
	start   int64
	boxType string
}

func openContainer(w io.WriteSeeker, boxType string) (*containerWriter, error) {
	start, err := w.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	hdr := make([]byte, 8)
	copy(hdr[4:8], boxType)
	if _, err := w.Write(hdr); err != nil {
		return nil, err
	}
	return &containerWriter{w: w, start: start, boxType: boxType}, nil
}

// openFullContainer writes a container box that also carries a version/
// flags field (a FullBox), e.g. stsd and dref. The four header bytes are
// part of the payload between the placeholder and the children.
func openFullContainer(w io.WriteSeeker, boxType string, version uint8, flags uint32) (*containerWriter, error) {
	cw, err := openContainer(w, boxType)
	if err != nil {
		return nil, err
	}
	fb := []byte{
		version,
		byte(flags >> 16),
		byte(flags >> 8),
		byte(flags),
	}
	if _, err := w.Write(fb); err != nil {
		return nil, err
	}
	return cw, nil
}

func (c *containerWriter) close() error {
	end, err := c.w.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	size := uint32(end - c.start)
	if _, err := c.w.Seek(c.start, io.SeekStart); err != nil {
		return err
	}
	if err := binary.Write(c.w, binary.BigEndian, size); err != nil {
		return err
	}
	_, err = c.w.Seek(end, io.SeekStart)
	return err
}

// writeU32 writes one big-endian uint32 field.
func writeU32(w io.Writer, v uint32) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	_, err := w.Write(b[:])
	return err
}
