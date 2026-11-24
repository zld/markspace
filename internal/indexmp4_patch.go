package medpipe

import (
	"encoding/binary"
	"errors"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// errTrunNotFound means the just-written fragment did not contain a trun
// box where the data offset patch was expected.
var errTrunNotFound = errors.New("trun box not found in serialized moof")

// writeMdatHeader writes the 8-byte mdat box header. The size is fixed at
// payloadSize+8 because every fragment is written to a dedicated file
// region; size 0 (extends to EOF) is avoided so fragmented outputs remain
// independently parseable after an interrupted run.
func writeMdatHeader(w io.Writer, payloadSize uint64) error {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[:4], uint32(payloadSize+8))
	copy(hdr[4:], []byte{'m', 'd', 'a', 't'})
	_, err := w.Write(hdr)
	return err
}

// patchTrunDataOffset rewrites only the data_offset field inside the trun
// box of an already serialized moof. Re-marshalling the whole moof would
// change nested box sizes; patching the fixed 32-bit field in place keeps
// every other byte identical.
func patchTrunDataOffset(r ReadWriteSeeker, f Fragment, dataOffset int32) error {
	// Locate moof > traf > trun structurally with go-mp4's extractor.
	paths := []mp4.BoxPath{
		{mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTrun()},
	}
	boxes, err := mp4.ExtractBoxes(r, nil, paths)
	if err != nil {
		return err
	}
	if len(boxes) != 1 {
		return errTrunNotFound
	}
	trunBI := boxes[0]
	// FullBox layout within trun: header (8) + version/flags (4) +
	// sample_count (4) + data_offset (4).
	fieldOff := trunBI.Offset + uint64(trunBI.HeaderSize) + 4 + 4
	if _, err := r.Seek(int64(fieldOff), io.SeekStart); err != nil {
		return err
	}
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(dataOffset))
	_, err = r.Write(buf[:])
	return err
}
