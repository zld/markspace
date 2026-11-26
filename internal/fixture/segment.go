// Package fixture deterministically generates local sample data
// (manifests, fMP4 segments, RTP event records) for demos and tests.
package fixture

import (
	"bytes"
	"encoding/binary"

	mp4 "github.com/abema/go-mp4"
)

// SegmentSpec describes one generated fMP4 segment.
type SegmentSpec struct {
	TrackID   uint32
	BaseTime  uint64   // tfdt base media decode time (source ticks)
	Durations []uint32 // trun per-sample durations (source ticks)
	Payload   []byte   // mdat payload
}

// BuildSegment serializes one fMP4 segment: ftyp, moof(mfhd,traf(tfhd,
// tfdt,trun)), mdat. Box headers are assembled around go-mp4-marshalled
// payloads.
func BuildSegment(spec SegmentSpec) ([]byte, error) {
	ftyp := &mp4.Ftyp{MajorBrand: [4]byte{'i', 's', 'o', 'm'}, MinorVersion: 1}
	ftyp.AddCompatibleBrand([4]byte{'i', 's', 'o', 'm'})
	ftyp.AddCompatibleBrand([4]byte{'c', 'm', 'f', 'c'})

	mfhd, err := payload(&mp4.Mfhd{SequenceNumber: 1})
	if err != nil {
		return nil, err
	}
	tfhd, err := payload(&mp4.Tfhd{TrackID: spec.TrackID})
	if err != nil {
		return nil, err
	}
	tfdt := &mp4.Tfdt{BaseMediaDecodeTimeV1: spec.BaseTime}
	tfdt.SetVersion(1)
	tfdtBytes, err := payload(tfdt)
	if err != nil {
		return nil, err
	}
	trun := &mp4.Trun{SampleCount: uint32(len(spec.Durations))}
	trun.SetFlags(0x000100) // sample-duration-present
	for _, d := range spec.Durations {
		trun.Entries = append(trun.Entries, mp4.TrunEntry{SampleDuration: d})
	}
	trunBytes, err := payload(trun)
	if err != nil {
		return nil, err
	}

	traf := box("traf", concat(box("tfhd", tfhd), box("tfdt", tfdtBytes), box("trun", trunBytes)))
	moof := box("moof", concat(box("mfhd", mfhd), traf))
	ftypBytes, err := payload(ftyp)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(box("ftyp", ftypBytes))
	out.Write(moof)
	out.Write(box("mdat", spec.Payload))
	return out.Bytes(), nil
}

// payload marshals one box's payload via go-mp4.
func payload(b mp4.IImmutableBox) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, b, mp4.Context{}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// box wraps a payload with a classic 8-byte box header.
func box(typ string, p []byte) []byte {
	out := make([]byte, 8, 8+len(p))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(p)))
	copy(out[4:8], typ)
	return append(out, p...)
}

func concat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}
