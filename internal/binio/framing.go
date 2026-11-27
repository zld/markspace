// Package binio holds the small framing primitives shared by the fixture
// writer and the demux readers: MP4 box framing and MXF-style BER length
// coding. Keeping them in one place guarantees producers and consumers
// interpret headers identically.
package binio

import (
	"encoding/binary"
	"errors"
	"io"

	"mediapack/internal/model"
)

// BERLen encodes length using the SMPTE KLV BER scheme (short form <128,
// long form otherwise).
func BERLen(length uint64) []byte {
	if length < 0x80 {
		return []byte{byte(length)}
	}
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], length)
	i := 0
	for i < 7 && raw[i] == 0 {
		i++
	}
	out := make([]byte, 0, 9-i)
	out = append(out, byte(0x80+(8-i)))
	out = append(out, raw[i:]...)
	return out
}

// ReadBERLen decodes a BER length prefix and returns it with the number of
// header bytes consumed.
func ReadBERLen(r io.ByteReader) (length uint64, header int, err error) {
	first, err := r.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	if first < 0x80 {
		return uint64(first), 1, nil
	}
	n := int(first & 0x7f)
	if n == 0 || n > 8 {
		return 0, 1, model.NewError(model.CodeMalformedContainer, "invalid BER length prefix")
	}
	var v uint64
	for i := 0; i < n; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, 1 + i, err
		}
		v = v<<8 | uint64(b)
	}
	return v, 1 + n, nil
}

// Box writes an MP4/ISO box (size, type, payload) and returns its file offset
// and total size.
func Box(w io.Writer, typ string, payload []byte) (offset, size int64, err error) {
	if len(typ) != 4 {
		return 0, 0, errors.New("box type must be 4 bytes")
	}
	if off, ok := w.(io.Seeker); ok {
		if offset, err = off.Seek(0, io.SeekCurrent); err != nil {
			return 0, 0, err
		}
	}
	head := make([]byte, 8)
	binary.BigEndian.PutUint32(head, uint32(8+len(payload)))
	copy(head[4:], typ)
	if _, err = w.Write(head); err != nil {
		return 0, 0, err
	}
	if len(payload) > 0 {
		if _, err = w.Write(payload); err != nil {
			return 0, 0, err
		}
	}
	return offset, int64(8 + len(payload)), nil
}

// FullBox prefixes version(1)+flags(3) to a payload.
func FullBox(version byte, flags uint32, body []byte) []byte {
	out := make([]byte, 0, 4+len(body))
	out = append(out, version, byte(flags>>16), byte(flags>>8), byte(flags))
	return append(out, body...)
}

// BE helpers for fixture builders.
func BE16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func BE32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func BE64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

// LimitReader returns a reader of exactly n bytes that flags truncation with
// a stable model error carrying the declared offset of the run. If the
// underlying reader ends early, the EOF is replaced by CodeTruncatedBox.
func LimitReader(r io.Reader, n int64, declaredAt int64) io.Reader {
	return &limitedReader{R: r, N: n, DeclaredAt: declaredAt}
}

type limitedReader struct {
	R          io.Reader
	N          int64
	DeclaredAt int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.N <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > l.N {
		p = p[:l.N]
	}
	// io.ReadFull semantics: tolerate readers that return partial data
	// without EOF, but a short final read means the declared run is truncated.
	total := 0
	for total < len(p) {
		n, err := l.R.Read(p[total:])
		total += n
		l.N -= int64(n)
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) && l.N > 0 {
			return total, model.NewError(model.CodeTruncatedBox,
				"declared run ends before EOF").WithOffset(l.DeclaredAt)
		}
		if errors.Is(err, io.EOF) && l.N == 0 {
			err = nil
		}
		return total, err
	}
	return total, nil
}
