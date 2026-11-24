package scan

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"archive/internal/model"
)

// MXF essence timestamps are carried inside the essence packet header on the
// RTP clock, so MXF tracks report a 90kHz timescale.
const mxfTimescale = model.RTPTimescale

var (
	// klvPrefix is the common SMPTE UL prefix of every KLV key.
	klvPrefix = []byte{0x06, 0x0E, 0x2B, 0x34}
	// partitionKeyPrefix matches any partition pack key up to the kind byte.
	partitionKeyPrefix = []byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x05, 0x01, 0x01,
		0x0D, 0x01, 0x02, 0x01, 0x01, 0x02}
	// essenceKeyPrefix matches generic-container essence element keys.
	essenceKeyPrefix = []byte{0x06, 0x0E, 0x2B, 0x34, 0x01, 0x02, 0x01, 0x01,
		0x0D, 0x01, 0x03, 0x01}
)

// ScanMXF walks the KLV stream of an MXF file: partition packs, essence
// packet boundaries and the track description recoverable from essence
// element keys. Damage is recorded with offsets; scanning resynchronises on
// the next KLV key whenever possible.
func ScanMXF(path string) (*Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	res := &Result{Format: "mxf", Size: st.Size()}
	tracks := map[int]*TrackMeta{}
	order := []int{}

	off := int64(0)
	for off+16+1 <= res.Size {
		var key [16]byte
		if _, err := f.ReadAt(key[:], off); err != nil {
			res.Errors = append(res.Errors, model.ScanError{Code: model.ErrTruncated, Offset: off, Track: -1, Detail: "KLV key cut off"})
			break
		}
		if !eq4(key[0:4], klvPrefix) {
			res.Errors = append(res.Errors, model.ScanError{Code: model.ErrCorruptBox, Offset: off, Track: -1, Detail: "invalid KLV key"})
			next := resyncKLV(f, off+1, res.Size)
			if next < 0 {
				break
			}
			off = next
			continue
		}
		length, lenBytes, ok := readBERLength(f, off+16, res.Size)
		if !ok {
			res.Errors = append(res.Errors, model.ScanError{Code: model.ErrTruncated, Offset: off, Track: -1, Detail: "KLV length cut off"})
			break
		}
		valOff := off + 16 + lenBytes
		if length < 0 || valOff+length > res.Size {
			res.Errors = append(res.Errors, model.ScanError{Code: model.ErrTruncated, Offset: off, Track: -1,
				Detail: fmt.Sprintf("KLV value of %d bytes extends past end of file", length)})
			break
		}
		switch {
		case eqN(key[:14], partitionKeyPrefix):
			res.Partitions = append(res.Partitions, off)
		case eqN(key[:12], essenceKeyPrefix):
			t := trackFor(tracks, &order, key)
			smp := SampleRef{Offset: valOff, Size: length}
			hdr := make([]byte, model.EssenceHdrSize)
			if _, err := f.ReadAt(hdr, valOff); err == nil {
				if h, ok := model.ParseEssenceHeader(hdr); ok {
					smp.PTS = h.PTS
				}
			}
			buf := make([]byte, length)
			if _, err := f.ReadAt(buf, valOff); err != nil || !model.ValidateEssence(buf) {
				res.Errors = append(res.Errors, model.ScanError{Code: model.ErrEssenceCRC, Offset: valOff, Track: t.ID, Detail: "essence integrity check failed"})
				smp.Bad = true
			}
			t.Samples = append(t.Samples, smp)
			if end := smp.PTS + 1; end > t.DurationUnits {
				t.DurationUnits = end
			}
		}
		off = valOff + length
	}
	if off < res.Size && res.Size-off >= 17 {
		// loop exited early without recording why (defensive; normally unreachable)
		res.Errors = append(res.Errors, model.ScanError{Code: model.ErrTruncated, Offset: off, Track: -1, Detail: "unparsed trailing data"})
	}
	for _, id := range order {
		t := tracks[id]
		res.Tracks = append(res.Tracks, *t)
		if d := t.DurationNs(); d > res.DurationNs {
			res.DurationNs = d
		}
	}
	return res, nil
}

// trackFor returns (creating on first sight) the track described by an
// essence element key: byte 12 is the item type (0x15 picture, 0x16 sound),
// byte 15 the element number, used as the track identifier.
func trackFor(tracks map[int]*TrackMeta, order *[]int, key [16]byte) *TrackMeta {
	id := int(key[15])
	if t, ok := tracks[id]; ok {
		return t
	}
	typ := "data"
	switch key[12] {
	case 0x15:
		typ = "video"
	case 0x16:
		typ = "audio"
	}
	t := &TrackMeta{ID: id, Type: typ, Timescale: mxfTimescale}
	tracks[id] = t
	*order = append(*order, id)
	return t
}

// readBERLength decodes a BER length at off. Returns the value length and
// the number of bytes the length field itself occupies.
func readBERLength(r io.ReaderAt, off, size int64) (length, lenBytes int64, ok bool) {
	var b [1]byte
	if _, err := r.ReadAt(b[:], off); err != nil {
		return 0, 0, false
	}
	if b[0] < 0x80 {
		return int64(b[0]), 1, true
	}
	n := int(b[0] & 0x7F)
	if n == 0 || n > 8 {
		return 0, 0, false
	}
	var buf [8]byte
	if _, err := r.ReadAt(buf[:n], off+1); err != nil {
		return 0, 0, false
	}
	for i := 0; i < n; i++ {
		length = length<<8 | int64(buf[i])
	}
	return length, int64(1 + n), true
}

// resyncKLV scans forward for the next KLV key prefix.
func resyncKLV(f *os.File, from, end int64) int64 {
	const chunk = 64 << 10
	buf := make([]byte, chunk+4)
	for base := from; base+4 <= end; base += chunk {
		n := int64(len(buf))
		if base+n > end {
			n = end - base
		}
		if _, err := f.ReadAt(buf[:n], base); err != nil && err != io.EOF {
			return -1
		}
		for i := int64(0); i+4 <= n; i++ {
			if eq4(buf[i:i+4], klvPrefix) {
				return base + i
			}
		}
	}
	return -1
}

func eq4(a, b []byte) bool { return binary.BigEndian.Uint32(a) == binary.BigEndian.Uint32(b) }

func eqN(a, b []byte) bool {
	if len(a) < len(b) {
		return false
	}
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
