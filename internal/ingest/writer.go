package ingest

import "encoding/binary"

// BuildSegment wraps a PCM payload in this pipeline's segment container:
// an ftyp box followed by a free box carrying the payload. The demo sample
// generator and tests use it to produce segments the Validator accepts.
func BuildSegment(payload []byte) []byte {
	var out []byte
	out = appendBox(out, "ftyp", []byte("mp42iso6mp41"))
	out = appendBox(out, "free", payload)
	return out
}

func appendBox(dst []byte, typ string, payload []byte) []byte {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(payload)+8))
	copy(hdr[4:], typ)
	dst = append(dst, hdr[:]...)
	return append(dst, payload...)
}
