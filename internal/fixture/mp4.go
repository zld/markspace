package fixture

import (
	"encoding/binary"
	"os"
	"path/filepath"
)

// MP4Sample describes one trun sample.
type MP4Sample struct {
	Duration uint32
	Size     uint32
}

// FragmentSpec is a tiny deterministic fMP4/CMAF fragment builder input.
type FragmentSpec struct {
	Sequence      uint32
	TrackID       uint32
	BaseTicks     uint64
	Samples       []MP4Sample
	ActualPayload []byte // nil means generate exactly the declared sample bytes
}

// WriteFragment builds ftyp + moof(mfhd,tfhd,tfdt,trun) + mdat.
func WriteFragment(path string, spec FragmentSpec) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	trun := buildTrun(0, spec.Samples)
	moof := buildMoof(spec, trun)
	dataOffset := uint32(len(moof) + 8)
	trun = buildTrun(int32(dataOffset), spec.Samples)
	moof = buildMoof(spec, trun)

	payload := spec.ActualPayload
	if payload == nil {
		var total uint32
		for _, s := range spec.Samples {
			total += s.Size
		}
		payload = patternBytes(int(total), byte(spec.Sequence))
	}
	mdat := box("mdat", payload)
	ftyp := buildFtyp()
	out := append([]byte{}, ftyp...)
	out = append(out, moof...)
	out = append(out, mdat...)
	return os.WriteFile(path, out, 0o644)
}

func buildFtyp() []byte {
	p := []byte("iso5")
	p = binary.BigEndian.AppendUint32(p, 512)
	p = append(p, []byte("iso5")...)
	p = append(p, []byte("iso6")...)
	p = append(p, []byte("mp41")...)
	return box("ftyp", p)
}

func buildMoof(spec FragmentSpec, trun []byte) []byte {
	mfhdPayload := binary.BigEndian.AppendUint32(nil, spec.Sequence)
	mfhd := fullBox("mfhd", 0, 0, mfhdPayload)

	tfhdPayload := binary.BigEndian.AppendUint32(nil, spec.TrackID)
	// default-base-is-moof makes trun data-offset relative to this moof.
	tfhd := fullBox("tfhd", 0, 0x020000, tfhdPayload)

	tfdtPayload := binary.BigEndian.AppendUint64(nil, spec.BaseTicks)
	tfdt := fullBox("tfdt", 1, 0, tfdtPayload)

	traf := box("traf", append(append(tfhd, tfdt...), trun...))
	return box("moof", append(mfhd, traf...))
}

func buildTrun(dataOffset int32, samples []MP4Sample) []byte {
	var p []byte
	p = binary.BigEndian.AppendUint32(p, uint32(len(samples)))
	p = binary.BigEndian.AppendUint32(p, uint32(dataOffset))
	for _, s := range samples {
		p = binary.BigEndian.AppendUint32(p, s.Duration)
		p = binary.BigEndian.AppendUint32(p, s.Size)
	}
	// data-offset-present + sample-duration-present + sample-size-present.
	return fullBox("trun", 0, 0x000301, p)
}

func box(typ string, payload []byte) []byte {
	size := uint32(8 + len(payload))
	out := make([]byte, 4, 8+len(payload))
	binary.BigEndian.PutUint32(out, size)
	out = append(out, typ...)
	out = append(out, payload...)
	return out
}

func fullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	vf := uint32(version)<<24 | flags&0x00ffffff
	head := binary.BigEndian.AppendUint32(nil, vf)
	return box(typ, append(head, payload...))
}

func patternBytes(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed ^ byte(i%251) ^ 0x5A
	}
	return b
}
