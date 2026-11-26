package index

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"

	"tsindex/internal/digest"
)

// Sidecar layout (ISO BMFF):
//
//	ftyp box                 written and enumerated via go-mp4
//	uuid box (extended type) hand-rolled header + 16-byte UUID + binary payload
//
// The binary payload is length-prefixed records so a truncated tail is
// detected by the verifier instead of being silently accepted.
var (
	majorBrand      = [4]byte{'t', 's', 'i', 'x'}
	compatBrand     = [4]byte{'i', 's', 'o', 'm'}
	boxTypeUUID     = mp4.StrToBoxType("uuid")
	// segmentUUID marks the per-segment digest+keyframe payload.
	segmentUUID = [16]byte{
		0x74, 0x73, 0x69, 0x6e, 0x64, 0x65, 0x78, 0x2d,
		0x73, 0x65, 0x67, 0x6d, 0x65, 0x6e, 0x74, 0x01,
	}
)

// SegmentRecord is everything persisted for one processed segment.
type SegmentRecord struct {
	SegmentID    int64
	StartOffset  int64
	EndOffset    int64
	Packets      int
	RawHash      [digest.Size]byte
	ContentHash  [digest.Size]byte
	Keyframes    []Keyframe
	Incomplete   bool // unsupported codec present: index is explicitly partial
}

// magic and version guard the payload format.
const (
	payloadMagic   = "TSIX"
	payloadVersion = byte(1)
)

// SidecarName returns the conventional sidecar filename for a source asset.
func SidecarName(sourceName string) string { return sourceName + ".tsix" }

// WriteSidecar serializes one record atomically: it writes to a temp file in
// the same directory, fsyncs, and renames over the target. The source file is
// never opened for writing and never overwritten.
func WriteSidecar(dir, sourceName string, rec SegmentRecord) (string, error) {
	payload, err := encodePayload(rec)
	if err != nil {
		return "", err
	}
	final := filepath.Join(dir, SidecarName(sourceName))
	tmp, err := os.CreateTemp(dir, ".tsix-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }

	w := mp4.NewWriter(tmp)
	// ftyp via the library's structured box API.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		cleanup()
		return "", err
	}
	ftyp := mp4.Ftyp{MajorBrand: majorBrand, MinorVersion: 1,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: compatBrand}}}
	if _, err := mp4.Marshal(tmp, &ftyp, mp4.Context{}); err != nil {
		cleanup()
		return "", err
	}
	if _, err := w.EndBox(); err != nil {
		cleanup()
		return "", err
	}
	if err := writeUUIDBox(tmp, segmentUUID, payload); err != nil {
		cleanup()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return final, nil
}

// writeUUIDBox emits an ISO BMFF extended-type box: size(4) + 'uuid'(4) +
// 16-byte extended type + payload. go-mp4 v1.7.4 has no native uuid box type,
// so the header is framed manually while the file remains a valid BMFF stream
// that the library's box enumerator can walk.
func writeUUIDBox(w io.Writer, uuid [16]byte, payload []byte) error {
	size := uint32(8 + 16 + len(payload))
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:], size)
	copy(hdr[4:], boxTypeUUID[:])
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := w.Write(uuid[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func encodePayload(rec SegmentRecord) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(payloadMagic)
	b.WriteByte(payloadVersion)
	putInt64(&b, rec.SegmentID)
	putInt64(&b, rec.StartOffset)
	putInt64(&b, rec.EndOffset)
	putInt32(&b, int32(rec.Packets))
	b.Write(rec.RawHash[:])
	b.Write(rec.ContentHash[:])
	putBool(&b, rec.Incomplete)
	putInt32(&b, int32(len(rec.Keyframes)))
	for _, kf := range rec.Keyframes {
		putInt16(&b, int16(kf.Program))
		putInt16(&b, int16(kf.TrackPID))
		putStr(&b, kf.Codec)
		putInt64(&b, kf.Ticks)
		putInt64(&b, kf.SourceOffset)
		putBool(&b, kf.Complete)
	}
	return b.Bytes(), nil
}

// ErrIncompleteSidecar signals a truncated or otherwise unparseable sidecar.
// The offset and recoverability let the caller resume rather than trust it.
var ErrIncompleteSidecar = errors.New("incomplete sidecar")

// ReadSidecar re-parses a sidecar through the go-mp4 box enumerator and
// decodes the uuid payload, validating every length and boundary.
func ReadSidecar(path string) (SegmentRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return SegmentRecord{}, err
	}
	defer f.Close()

	var payload []byte
	foundFtyp := false
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeFtyp():
			foundFtyp = true
		case boxTypeUUID:
			var buf bytes.Buffer
			if _, rerr := h.ReadData(&buf); rerr != nil {
				return nil, rerr
			}
			raw := buf.Bytes()
			if len(raw) < 16 {
				return nil, fmt.Errorf("%w: uuid box too short", ErrIncompleteSidecar)
			}
			if bytes.Equal(raw[:16], segmentUUID[:]) {
				payload = raw[16:]
			}
		}
		return nil, nil
	})
	if err != nil {
		return SegmentRecord{}, fmt.Errorf("%w: %v", ErrIncompleteSidecar, err)
	}
	if !foundFtyp {
		return SegmentRecord{}, fmt.Errorf("%w: missing ftyp box", ErrIncompleteSidecar)
	}
	if payload == nil {
		return SegmentRecord{}, fmt.Errorf("%w: missing segment uuid box", ErrIncompleteSidecar)
	}
	return decodePayload(payload)
}

func decodePayload(p []byte) (SegmentRecord, error) {
	var rec SegmentRecord
	r := bytes.NewReader(p)
	magic := make([]byte, 4)
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != payloadMagic {
		return rec, fmt.Errorf("%w: bad magic", ErrIncompleteSidecar)
	}
	ver, err := r.ReadByte()
	if err != nil || ver != payloadVersion {
		return rec, fmt.Errorf("%w: unsupported version", ErrIncompleteSidecar)
	}
	if rec.SegmentID, err = getInt64(r); err != nil {
		return rec, wrap(err)
	}
	if rec.StartOffset, err = getInt64(r); err != nil {
		return rec, wrap(err)
	}
	if rec.EndOffset, err = getInt64(r); err != nil {
		return rec, wrap(err)
	}
	n32, err := getInt32(r)
	if err != nil {
		return rec, wrap(err)
	}
	rec.Packets = int(n32)
	if _, err = io.ReadFull(r, rec.RawHash[:]); err != nil {
		return rec, wrap(err)
	}
	if _, err = io.ReadFull(r, rec.ContentHash[:]); err != nil {
		return rec, wrap(err)
	}
	if rec.Incomplete, err = getBool(r); err != nil {
		return rec, wrap(err)
	}
	nk, err := getInt32(r)
	if err != nil {
		return rec, wrap(err)
	}
	if nk < 0 || nk > 1_000_000 {
		return rec, fmt.Errorf("%w: implausible keyframe count", ErrIncompleteSidecar)
	}
	rec.Keyframes = make([]Keyframe, 0, nk)
	for i := int32(0); i < nk; i++ {
		kf, kerr := readKeyframe(r)
		if kerr != nil {
			return rec, kerr
		}
		rec.Keyframes = append(rec.Keyframes, kf)
	}
	return rec, nil
}

func readKeyframe(r *bytes.Reader) (Keyframe, error) {
	var kf Keyframe
	prog, err := getInt16(r)
	if err != nil {
		return kf, wrap(err)
	}
	pid, err := getInt16(r)
	if err != nil {
		return kf, wrap(err)
	}
	codec, err := getStr(r)
	if err != nil {
		return kf, wrap(err)
	}
	if kf.Ticks, err = getInt64(r); err != nil {
		return kf, wrap(err)
	}
	if kf.SourceOffset, err = getInt64(r); err != nil {
		return kf, wrap(err)
	}
	if kf.Complete, err = getBool(r); err != nil {
		return kf, wrap(err)
	}
	kf.Program = uint16(prog)
	kf.TrackPID = uint16(pid)
	kf.Codec = codec
	return kf, nil
}

func wrap(err error) error { return fmt.Errorf("%w: %v", ErrIncompleteSidecar, err) }

func putInt64(b *bytes.Buffer, v int64) { var tmp [8]byte; binary.BigEndian.PutUint64(tmp[:], uint64(v)); b.Write(tmp[:]) }
func putInt32(b *bytes.Buffer, v int32) { var tmp [4]byte; binary.BigEndian.PutUint32(tmp[:], uint32(v)); b.Write(tmp[:]) }
func putInt16(b *bytes.Buffer, v int16) { var tmp [2]byte; binary.BigEndian.PutUint16(tmp[:], uint16(v)); b.Write(tmp[:]) }
func putBool(b *bytes.Buffer, v bool) {
	if v {
		b.WriteByte(1)
	} else {
		b.WriteByte(0)
	}
}
func putStr(b *bytes.Buffer, s string) { putInt32(b, int32(len(s))); b.WriteString(s) }

func getInt64(r io.Reader) (int64, error) { var tmp [8]byte; _, err := io.ReadFull(r, tmp[:]); return int64(binary.BigEndian.Uint64(tmp[:])), err }
func getInt32(r io.Reader) (int32, error) { var tmp [4]byte; _, err := io.ReadFull(r, tmp[:]); return int32(binary.BigEndian.Uint32(tmp[:])), err }
func getInt16(r io.Reader) (int16, error) { var tmp [2]byte; _, err := io.ReadFull(r, tmp[:]); return int16(binary.BigEndian.Uint16(tmp[:])), err }
func getBool(r io.Reader) (bool, error) {
	var tmp [1]byte
	if _, err := io.ReadFull(r, tmp[:]); err != nil {
		return false, err
	}
	return tmp[0] != 0, nil
}
func getStr(r *bytes.Reader) (string, error) {
	n, err := getInt32(r)
	if err != nil {
		return "", err
	}
	if n < 0 || int(n) > r.Len() {
		return "", fmt.Errorf("%w: string length out of range", ErrIncompleteSidecar)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", wrap(err)
	}
	return string(buf), nil
}
