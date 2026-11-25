package fixture

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"
)

// SampleSpec is one trun sample.
type SampleSpec struct {
	Duration uint32
	Size     uint32
}

// FragmentSpec describes a small generated fMP4 segment.
type FragmentSpec struct {
	Sequence       uint32
	BaseDecodeTime uint64
	Samples        []SampleSpec
}

// FragmentInfo records absolute offsets inside the emitted segment bytes.
type FragmentInfo struct {
	MoofOffset    int64
	DataOffset    int64
	SampleOffsets []int64
	Bytes         []byte
}

func box(typ string, payload ...[]byte) []byte {
	var p []byte
	for _, b := range payload {
		p = append(p, b...)
	}
	out := make([]byte, 8+len(p))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], p)
	return out
}

func fullPayload(version byte, flags uint32, body ...[]byte) []byte {
	out := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	for _, b := range body {
		out = append(out, b...)
	}
	return out
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func marshalBox(b mp4.IImmutableBox) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, b, mp4.Context{}); err != nil {
		return nil, err
	}
	return box(b.GetType().String(), buf.Bytes()), nil
}

// WriteInitMP4 writes a minimal init segment containing mdhd with timescale.
func WriteInitMP4(path string, timescale uint32) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	ftypPayload := append([]byte("iso5"), u32(0x200)...)
	ftypPayload = append(ftypPayload, []byte("iso5dashmp42")...)
	mdhd, err := marshalBox(&mp4.Mdhd{
		FullBox:    mp4.FullBox{Version: 0},
		Timescale:  timescale,
		DurationV0: 0,
	})
	if err != nil {
		return err
	}
	moov := box("moov", box("trak", box("mdia", mdhd)))
	return os.WriteFile(path, append(box("ftyp", ftypPayload), moov...), 0o644)
}

// BuildFragment constructs a parseable moof+mdat segment and returns sample offsets.
func BuildFragment(spec FragmentSpec) (*FragmentInfo, error) {
	mfhd := box("mfhd", fullPayload(0, 0, u32(spec.Sequence)))
	tfhd := box("tfhd", fullPayload(0, 0, u32(1)))

	tfdtBox, err := marshalBox(&mp4.Tfdt{
		FullBox:               mp4.FullBox{Version: 1},
		BaseMediaDecodeTimeV1: spec.BaseDecodeTime,
	})
	if err != nil {
		return nil, err
	}

	entries := make([]mp4.TrunEntry, 0, len(spec.Samples))
	var payloadSize int64
	for _, s := range spec.Samples {
		entries = append(entries, mp4.TrunEntry{SampleDuration: s.Duration, SampleSize: s.Size})
		payloadSize += int64(s.Size)
	}
	trun := &mp4.Trun{
		FullBox:     mp4.FullBox{Version: 1, Flags: [3]byte{0x00, 0x03, 0x01}},
		SampleCount: uint32(len(entries)),
		DataOffset:  0,
		Entries:     entries,
	}
	trunBox, err := marshalBox(trun)
	if err != nil {
		return nil, err
	}
	traf := box("traf", tfhd, tfdtBox, trunBox)
	moof := box("moof", mfhd, traf)
	moofSize := int64(len(moof))
	dataOffset := moofSize + 8 // + mdat header

	trun.DataOffset = int32(dataOffset)
	trunBox, err = marshalBox(trun)
	if err != nil {
		return nil, err
	}
	traf = box("traf", tfhd, tfdtBox, trunBox)
	moof = box("moof", mfhd, traf)
	if int64(len(moof)) != moofSize {
		return nil, &buildError{"moof size changed after setting data offset"}
	}

	mdatPayload := make([]byte, payloadSize)
	for i := range mdatPayload {
		mdatPayload[i] = byte('A' + i%26)
	}
	mdat := box("mdat", mdatPayload)
	out := append(append([]byte{}, moof...), mdat...)

	offsets := make([]int64, 0, len(spec.Samples))
	cursor := dataOffset
	for _, s := range spec.Samples {
		offsets = append(offsets, cursor)
		cursor += int64(s.Size)
	}
	return &FragmentInfo{
		MoofOffset:    0,
		DataOffset:    dataOffset,
		SampleOffsets: offsets,
		Bytes:         out,
	}, nil
}

func WriteFragment(path string, spec FragmentSpec) (*FragmentInfo, error) {
	info, err := BuildFragment(spec)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, info.Bytes, 0o644); err != nil {
		return nil, err
	}
	return info, nil
}

type buildError struct{ msg string }

func (e *buildError) Error() string { return e.msg }
