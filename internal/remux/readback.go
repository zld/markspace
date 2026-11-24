// Package remux: reading back emitted segments for verification.
package remux

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"
)

// mdatBox locates the media data box inside an emitted segment.
func mdatRange(path string) (off, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	bis, err := mp4.ExtractBoxes(f, nil, []mp4.BoxPath{{mp4.BoxTypeMdat()}})
	if err != nil {
		return 0, 0, err
	}
	if len(bis) != 1 {
		return 0, 0, fmt.Errorf("remux: expected one mdat, found %d", len(bis))
	}
	bi := bis[0]
	return int64(bi.Offset + bi.HeaderSize), int64(bi.Size - bi.HeaderSize), nil
}

// ReadSegmentPayloads returns the SHA-256 of each elementary sample inside a
// segment's mdat, in mdat order (video samples first, then audio, matching
// how the writer lays them out). The sample sizes are read from each track's
// stsz, so a truncated or shifted mdat is detected rather than guessed.
func ReadSegmentPayloads(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sizes, err := trackSampleSizes(f)
	if err != nil {
		return nil, err
	}
	mdatOff, mdatSize, err := mdatRange(path)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(mdatOff, io.SeekStart); err != nil {
		return nil, err
	}
	var out []string
	var consumed int64
	for _, list := range sizes {
		for _, sz := range list {
			if consumed+int64(sz) > mdatSize {
				return nil, fmt.Errorf("remux: mdat shorter than stsz totals at sample %d", len(out))
			}
			buf := make([]byte, sz)
			if _, err := io.ReadFull(f, buf); err != nil {
				return nil, err
			}
			sum := sha256.Sum256(buf)
			out = append(out, hex.EncodeToString(sum[:]))
			consumed += int64(sz)
		}
	}
	return out, nil
}

// findRawBox locates a child box by its four-character type inside a parent
// box. It exists for box types go-mp4 does not model, such as our provenance
// uuid. It returns the child's payload (after the 8-byte header).
func findRawBox(f *os.File, parent mp4.BoxType, child string) ([]byte, error) {
	// Walk top-level boxes until the parent appears, so this works regardless
	// of whether ftyp or moov comes first.
	var bi *mp4.BoxInfo
	for {
		info, err := mp4.ReadBoxInfo(f)
		if err != nil {
			return nil, err
		}
		if info.Type == parent {
			bi = info
			break
		}
		if _, err := f.Seek(int64(info.Size-info.HeaderSize), io.SeekCurrent); err != nil {
			return nil, err
		}
	}
	payloadSize := int64(bi.Size - bi.HeaderSize)
	buf := make([]byte, payloadSize)
	if _, err := f.ReadAt(buf, int64(bi.Offset+bi.HeaderSize)); err != nil {
		return nil, err
	}
	for off := 0; off+8 <= len(buf); {
		size := int64(binary.BigEndian.Uint32(buf[off : off+4]))
		typ := string(buf[off+4 : off+8])
		if size < 8 || off+int(size) > len(buf) {
			return nil, fmt.Errorf("remux: malformed child box at %d", off)
		}
		if typ == child {
			return buf[off+8 : off+int(size)], nil
		}
		off += int(size)
	}
	return nil, fmt.Errorf("remux: box %q not found inside %s", child, parent)
}

// trackSampleSizes reads stsz of every track in file order.
func trackSampleSizes(f *os.File) ([][]uint32, error) {
	paths := []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsz()},
	}
	bips, err := mp4.ExtractBoxesWithPayload(f, nil, paths)
	if err != nil {
		return nil, err
	}
	var out [][]uint32
	for _, bip := range bips {
		stsz, ok := bip.Payload.(*mp4.Stsz)
		if !ok {
			return nil, fmt.Errorf("remux: unexpected stsz payload %T", bip.Payload)
		}
		sizes := make([]uint32, 0, stsz.SampleCount)
		if stsz.SampleSize != 0 {
			// A constant sample size is valid but the writer never emits it;
			// expand it anyway so a foreign segment is still readable.
			for i := uint32(0); i < stsz.SampleCount; i++ {
				sizes = append(sizes, stsz.SampleSize)
			}
		} else {
			sizes = append(sizes, stsz.EntrySize...)
		}
		out = append(out, sizes)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("remux: no stsz found")
	}
	return out, nil
}
