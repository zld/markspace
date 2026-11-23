// Package mp4index uses go-mp4 to build a byte-exact ISO-BMFF init segment that
// describes the same time base as an aligned representation. It does not
// re-encode media: only timing metadata boxes are written.
package mp4index

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	mp4 "github.com/abema/go-mp4"
)

// TrackInfo describes one representation's track for the init segment.
type TrackInfo struct {
	TrackID    uint32
	Timescale  uint32
	DurationMS uint64
	Handler    string // "vide" or "soun"
	Width      uint32
	Height     uint32
	Codec      string
}

// BuildInitSegment writes ftyp, moov(mvhd, trak(tkhd, mdia(mdhd, hdlr, minf(stbl(empty)))))
// with go-mp4's marshaller. The returned bytes are deterministic for a given
// input, which lets the pipeline hash them as an artifact.
func BuildInitSegment(tracks []TrackInfo) ([]byte, error) {
	if len(tracks) == 0 {
		return nil, fmt.Errorf("mp4index: no tracks")
	}
	var out bytes.Buffer
	ftyp := &mp4.Ftyp{MajorBrand: mp4.BrandISO8(), MinorVersion: 512}
	ftyp.AddCompatibleBrand(mp4.BrandISO8())
	if err := writeBox(&out, ftyp, mp4.Context{}); err != nil {
		return nil, err
	}
	var moovPayload bytes.Buffer
	// Movie timescale fixed at 1000ms for all representations.
	var maxDur uint64
	for _, t := range tracks {
		if t.DurationMS > maxDur {
			maxDur = t.DurationMS
		}
	}
	mvhd := &mp4.Mvhd{
		Timescale:   1000,
		DurationV0:  uint32(maxDur),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix(),
		NextTrackID: uint32(len(tracks) + 1),
	}
	if err := writeBox(&moovPayload, mvhd, mp4.Context{}); err != nil {
		return nil, err
	}
	for _, t := range tracks {
		trak, err := buildTrak(t)
		if err != nil {
			return nil, err
		}
		moovPayload.Write(trak)
	}
	if err := writeContainer(&out, mp4.BoxTypeMoov(), moovPayload.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func buildTrak(t TrackInfo) ([]byte, error) {
	var trakPayload bytes.Buffer
	tkhd := &mp4.Tkhd{
		TrackID:    t.TrackID,
		DurationV0: uint32(t.DurationMS),
		Volume:     tkhdVolume(t.Handler),
		Matrix:     identityMatrix(),
		Width:      t.Width << 16,
		Height:     t.Height << 16,
	}
	if err := writeBox(&trakPayload, tkhd, mp4.Context{}); err != nil {
		return nil, err
	}
	var mdiaPayload bytes.Buffer
	mdhd := &mp4.Mdhd{Timescale: t.Timescale, DurationV0: uint32(t.DurationMS), Language: [3]byte{'u', 'n', 'd'}}
	if err := writeBox(&mdiaPayload, mdhd, mp4.Context{}); err != nil {
		return nil, err
	}
	var hdlrBytes bytes.Buffer
	hdlr := &mp4.Hdlr{HandlerType: handlerBytes(t.Handler), Name: "mediad"}
	if err := writeBox(&hdlrBytes, hdlr, mp4.Context{}); err != nil {
		return nil, err
	}
	mdiaPayload.Write(hdlrBytes.Bytes())
	var minfPayload bytes.Buffer
	if err := writeContainer(&minfPayload, mp4.BoxTypeVmhd(), []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		return nil, err
	}
	var stbl bytes.Buffer
	if err := writeContainer(&stbl, mp4.BoxTypeStbl(), nil); err != nil {
		return nil, err
	}
	var dinf bytes.Buffer
	if err := writeContainer(&dinf, mp4.BoxTypeDinf(), nil); err != nil {
		return nil, err
	}
	minfPayload.Write(dinf.Bytes())
	minfPayload.Write(stbl.Bytes())
	if err := writeContainer(&mdiaPayload, mp4.BoxTypeMinf(), minfPayload.Bytes()); err != nil {
		return nil, err
	}
	if err := writeContainer(&trakPayload, mp4.BoxTypeMdia(), mdiaPayload.Bytes()); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeContainer(&out, mp4.BoxTypeTrak(), trakPayload.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

func tkhdVolume(handler string) int16 {
	if handler == "soun" {
		return 0x0100
	}
	return 0
}

func handlerBytes(h string) [4]byte {
	var out [4]byte
	copy(out[:], h)
	return out
}

func writeBox(w io.Writer, box mp4.IImmutableBox, ctx mp4.Context) error {
	var payload bytes.Buffer
	if _, err := mp4.Marshal(&payload, box, ctx); err != nil {
		return fmt.Errorf("mp4index: marshal %s: %w", box.GetType(), err)
	}
	return writeContainer(w, box.GetType(), payload.Bytes())
}

func writeContainer(w io.Writer, typ mp4.BoxType, payload []byte) error {
	size := uint32(8 + len(payload))
	if size < 8 {
		return fmt.Errorf("mp4index: box too large")
	}
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], size)
	copy(hdr[4:], typ[:])
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ParseInitSegment re-reads the init segment with go-mp4 and returns the movie
// timescale and per-track timescales. The verifier uses this to confirm the
// time base matches the manifest instead of trusting the bytes it wrote.
func ParseInitSegment(data []byte) (movieTimescale uint32, trackTimescales map[uint32]uint32, err error) {
	rs := bytes.NewReader(data)
	boxes, err := mp4.ExtractBoxWithPayload(rs, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeMvhd()})
	if err != nil {
		return 0, nil, fmt.Errorf("mp4index: read mvhd: %w", err)
	}
	if len(boxes) != 1 {
		return 0, nil, fmt.Errorf("mp4index: expected one mvhd, got %d", len(boxes))
	}
	mvhd, ok := boxes[0].Payload.(*mp4.Mvhd)
	if !ok {
		return 0, nil, fmt.Errorf("mp4index: mvhd payload type %T", boxes[0].Payload)
	}
	movieTimescale = mvhd.Timescale
	trackTimescales = map[uint32]uint32{}
	mdhds, err := mp4.ExtractBoxWithPayload(rs, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()})
	if err != nil {
		return 0, nil, fmt.Errorf("mp4index: read mdhd: %w", err)
	}
	for _, b := range mdhds {
		mdhd, ok := b.Payload.(*mp4.Mdhd)
		if !ok {
			continue
		}
		trackTimescales[mdhd.Timescale]++
	}
	return movieTimescale, trackTimescales, nil
}

// NanoToTimescale converts nanoseconds to a track timescale, rounding down.
// Integer arithmetic avoids float drift at millisecond precision.
func NanoToTimescale(nanos int64, timescale uint32) uint64 {
	if nanos < 0 || timescale == 0 {
		return 0
	}
	return uint64(nanos) * uint64(timescale) / uint64(time.Second)
}
