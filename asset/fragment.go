// Package asset maps multiple media-asset versions onto program/track/interval
// keys, parses CMAF fragments for PTS/DTS frame boundaries, and reports gaps
// and content conflicts without inventing missing media.
package asset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"
)

// Sample is one frame (video) or audio access unit on the unified time base.
type Sample struct {
	Index       int    `json:"index"`
	PTSNS       int64  `json:"pts_ns"`
	DTSNS       int64  `json:"dts_ns"`
	DurationNS  int64  `json:"duration_ns"`
	Size        uint32 `json:"size"`
	PayloadOff  int64  `json:"payload_offset"` // offset inside mdat payload
}

// Fragment is the parsed view of one fMP4/CMAF media segment.
type Fragment struct {
	TrackID       uint32   `json:"track_id"`
	Timescale     uint64   `json:"timescale"`
	Sequence      uint32   `json:"sequence"`
	BaseDecodeTS  uint64   `json:"base_decode_ts"`
	Samples       []Sample `json:"samples"`
	ContentSHA256 string   `json:"content_sha256"` // hash of concatenated sample bytes
	Size          int64    `json:"size"`
}

// offsetReader records the highest byte offset consumed so truncation points
// can be reported as byte offsets.
type offsetReader struct {
	r   io.ReadSeeker
	pos int64
}

func (o *offsetReader) Read(p []byte) (int, error) {
	n, err := o.r.Read(p)
	o.pos += int64(n)
	return n, err
}

func (o *offsetReader) Seek(off int64, whence int) (int64, error) {
	p, err := o.r.Seek(off, whence)
	if err == nil {
		o.pos = p
	}
	return p, err
}

// ParseFragment parses a CMAF fragment from r using the track timescale from
// the initialization segment. The reader is consumed sequentially and only
// sample metadata is retained, so memory is bounded per segment.
func ParseFragment(r io.ReadSeeker, timescale uint64) (*Fragment, error) {
	if timescale == 0 {
		return nil, errors.New("zero timescale in fragment parse")
	}
	or := &offsetReader{r: r}
	f := &Fragment{Timescale: timescale}
	var (
		trackID  uint32
		tfdt     *mp4.Tfdt
		trun     *mp4.Trun
		mfhd     *mp4.Mfhd
		mdatData []byte
	)
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		path := h.Path
		last := path[len(path)-1]
		switch {
		case last == mp4.BoxTypeMfhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			mfhd = box.(*mp4.Mfhd)
			return nil, nil
		case last == mp4.BoxTypeTfhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			trackID = box.(*mp4.Tfhd).TrackID
			return nil, nil
		case last == mp4.BoxTypeTfdt():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			tfdt = box.(*mp4.Tfdt)
			return nil, nil
		case last == mp4.BoxTypeTrun():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			trun = box.(*mp4.Trun)
			return nil, nil
		case last == mp4.BoxTypeMdat():
			// mdat payload is read once and hashed, then released.
			var buf bytes.Buffer
			if _, err := h.ReadData(&buf); err != nil {
				return nil, err
			}
			mdatData = buf.Bytes()
			return nil, nil
		}
		return h.Expand()
	}
	if _, err := mp4.ReadBoxStructure(or, handler); err != nil {
		return nil, fmt.Errorf("fragment parse at byte %d: %w", or.pos, err)
	}
	if tfdt == nil || trun == nil {
		return nil, fmt.Errorf("fragment missing tfdt/trun at byte %d", or.pos)
	}
	if mfhd != nil {
		f.Sequence = mfhd.SequenceNumber
	}
	f.TrackID = trackID
	f.BaseDecodeTS = tfdt.GetBaseMediaDecodeTime()
	if err := buildSamples(f, trun, mdatData, or.pos); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(mdatData)
	f.ContentSHA256 = hex.EncodeToString(hash[:])
	if end, err := r.Seek(0, io.SeekEnd); err == nil {
		f.Size = end
	}
	return f, nil
}

func buildSamples(f *Fragment, trun *mp4.Trun, mdat []byte, readPos int64) error {
	dts := int64(f.BaseDecodeTS)
	var payloadOff int64
	for i, e := range trun.Entries {
		dtsNS, err := scaleNS(dts, f.Timescale)
		if err != nil {
			return fmt.Errorf("sample %d dts overflow at byte %d: %w", i, readPos, err)
		}
		// Composition time offset is signed in version-1 trun boxes.
		cts := int64(e.SampleCompositionTimeOffsetV1)
		ptsUnits := dts + cts
		if ptsUnits < 0 {
			return fmt.Errorf("sample %d negative PTS at byte %d", i, readPos)
		}
		ptsNS, err := scaleNS(ptsUnits, f.Timescale)
		if err != nil {
			return fmt.Errorf("sample %d pts overflow: %w", i, err)
		}
		durNS, err := scaleNS(int64(e.SampleDuration), f.Timescale)
		if err != nil {
			return fmt.Errorf("sample %d duration overflow: %w", i, err)
		}
		if int64(e.SampleSize) > int64(len(mdat))-payloadOff {
			return fmt.Errorf("truncated fragment at byte %d: sample %d exceeds mdat", readPos, i)
		}
		f.Samples = append(f.Samples, Sample{
			Index:      i,
			PTSNS:      ptsNS,
			DTSNS:      dtsNS,
			DurationNS: durNS,
			Size:       e.SampleSize,
			PayloadOff: payloadOff,
		})
		dts += int64(e.SampleDuration)
		payloadOff += int64(e.SampleSize)
	}
	return nil
}

func scaleNS(units int64, timescale uint64) (int64, error) {
	if units < 0 || timescale == 0 {
		return 0, errors.New("illegal scale input")
	}
	u := uint64(units)
	const maxUnits = (1 << 63) / 1_000_000_000
	if u > maxUnits {
		return 0, errors.New("nanosecond overflow")
	}
	return int64((u * 1_000_000_000) / timescale), nil
}
