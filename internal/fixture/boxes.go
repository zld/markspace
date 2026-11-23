package fixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/abema/go-mp4"
)

// TrackPlan is the mutable logical form of one fragment. Injection mutates
// plans and re-serializes, so every reported offset matches the produced bytes.
type TrackPlan struct {
	Track   TrackSpec
	Seq     uint32
	Tfdt    int64
	Samples []PlanSample
}

// PlanSample is one logical sample before serialization.
type PlanSample struct {
	Start    int64
	Duration int64
	Payload  []byte
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

func payload(b mp4.IImmutableBox) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, b, mp4.Context{}); err != nil {
		return nil, fmt.Errorf("marshal %s: %w", b.GetType(), err)
	}
	return buf.Bytes(), nil
}

func mustPayload(b mp4.IImmutableBox) []byte {
	p, err := payload(b)
	if err != nil {
		panic(err)
	}
	return p
}

func container(typ string, children ...[]byte) []byte {
	n := 8
	for _, c := range children {
		n += len(c)
	}
	out := make([]byte, 8, n)
	binary.BigEndian.PutUint32(out, uint32(n))
	copy(out[4:], typ)
	for _, c := range children {
		out = append(out, c...)
	}
	return out
}

func matrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

// BuildInit returns ftyp+moov for the spec.
func BuildInit(spec Spec) ([]byte, error) {
	ftyp, err := payload(&mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', '6'},
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', '6'}},
			{CompatibleBrand: [4]byte{'c', 'm', 'f', 'f'}},
		},
	})
	if err != nil {
		return nil, err
	}
	mvhd, err := payload(&mp4.Mvhd{
		Timescale:   spec.TimelineTimescale,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      matrix(),
		NextTrackID: uint32(len(spec.Tracks) + 1),
	})
	if err != nil {
		return nil, err
	}
	children := [][]byte{container("mvhd", mvhd)}
	var trexes [][]byte
	for _, t := range spec.Tracks {
		trak, err := buildTrak(t)
		if err != nil {
			return nil, err
		}
		children = append(children, trak)
		trex, err := payload(&mp4.Trex{
			TrackID:                       t.ID,
			DefaultSampleDescriptionIndex: 1,
			DefaultSampleDuration:         t.SampleDuration,
			DefaultSampleSize:             100,
			DefaultSampleFlags:            0x02000000,
		})
		if err != nil {
			return nil, err
		}
		trexes = append(trexes, container("trex", trex))
	}
	children = append(children, container("mvex", trexes...))
	return append(container("ftyp", ftyp), container("moov", children...)...), nil
}

func buildTrak(t TrackSpec) ([]byte, error) {
	var handler [4]byte
	volume := int16(0)
	width, height := uint32(0), uint32(0)
	if t.Kind == "video" {
		handler = [4]byte{'v', 'i', 'd', 'e'}
		width, height = t.Width<<16, t.Height<<16
	} else {
		handler = [4]byte{'s', 'o', 'u', 'n'}
		volume = 0x0100
	}
	tkhd, err := payload(&mp4.Tkhd{TrackID: t.ID, Volume: volume, Matrix: matrix(), Width: width, Height: height})
	if err != nil {
		return nil, err
	}
	mdhd, err := payload(&mp4.Mdhd{Timescale: t.Timescale, Language: [3]byte{'u', 'n', 'd'}})
	if err != nil {
		return nil, err
	}
	hdlr, err := payload(&mp4.Hdlr{HandlerType: handler, Reserved: [3]uint32{}, Name: "Handler\x00"})
	if err != nil {
		return nil, err
	}
	url := &mp4.Url{}
	url.AddFlag(mp4.UrlSelfContained)
	dref := container("dref", mustPayload(&mp4.Dref{EntryCount: 1}), container("url ", mustPayload(url)))
	stbl := container("stbl",
		container("stsd", mustPayload(&mp4.Stsd{})),
		container("stts", mustPayload(&mp4.Stts{})),
		container("stsc", mustPayload(&mp4.Stsc{})),
		container("stsz", mustPayload(&mp4.Stsz{})),
		container("stco", mustPayload(&mp4.Stco{})),
	)
	var mediaHeader []byte
	if t.Kind == "video" {
		mediaHeader = container("vmhd", mustPayload(&mp4.Vmhd{}))
	} else {
		mediaHeader = container("smhd", mustPayload(&mp4.Smhd{}))
	}
	minf := container("minf", mediaHeader, container("dinf", dref), stbl)
	mdia := container("mdia", container("mdhd", mdhd), container("hdlr", hdlr), minf)
	return container("trak", container("tkhd", tkhd), mdia), nil
}

type fragLayout struct {
	tfdtOffset       int64
	trunOffset       int64
	sampleOffsets    []int64
	fragmentDuration int64
	trackID          uint32
	sequence         uint32
	sampleCount      uint32
}

// buildFragment serializes one moof/mdat pair. dataOffset is the trun data
// offset to the first sample byte, measured from the trun payload start.
func buildFragment(p TrackPlan, dataOffset int32) (moof, mdat []byte, layout fragLayout, err error) {
	layout.trackID = p.Track.ID
	layout.sequence = p.Seq
	layout.sampleCount = uint32(len(p.Samples))
	mfhd := container("mfhd", mustPayload(&mp4.Mfhd{SequenceNumber: p.Seq}))
	tfdtBox := &mp4.Tfdt{BaseMediaDecodeTimeV1: uint64(p.Tfdt)}
	tfdtBox.SetVersion(1)
	tfdt := container("tfdt", mustPayload(tfdtBox))
	tfhd := &mp4.Tfhd{TrackID: p.Track.ID}
	tfhd.AddFlag(mp4.TfhdDefaultBaseIsMoof)
	tfhdBox := container("tfhd", mustPayload(tfhd))
	// moof(8) + mfhd + traf(8) + tfhd are skipped to reach tfdt.
	layout.tfdtOffset = int64(8 + len(mfhd) + 8 + len(tfhdBox))
	trun := &mp4.Trun{SampleCount: uint32(len(p.Samples)), DataOffset: dataOffset}
	trun.AddFlag(0x000001 | 0x000100 | 0x000200)
	var raw []byte
	var cursor int64
	for _, s := range p.Samples {
		trun.Entries = append(trun.Entries, mp4.TrunEntry{SampleDuration: uint32(s.Duration), SampleSize: uint32(len(s.Payload))})
		layout.sampleOffsets = append(layout.sampleOffsets, cursor)
		cursor += int64(len(s.Payload))
		raw = append(raw, s.Payload...)
		layout.fragmentDuration += s.Duration
	}
	layout.trunOffset = int64(8 + len(mfhd) + 8 + len(tfhdBox) + len(tfdt))
	trunBox := container("trun", mustPayload(trun))
	traf := container("traf", tfhdBox, tfdt, trunBox)
	moof = container("moof", mfhd, traf)
	mdat = container("mdat", raw)
	return moof, mdat, layout, nil
}

func buildSidx(timescale uint32, refs []int64, durations []uint32) ([]byte, error) {
	s := &mp4.Sidx{ReferenceID: 1, Timescale: timescale, ReferenceCount: uint16(len(refs))}
	for i, size := range refs {
		s.References = append(s.References, mp4.SidxReference{
			ReferencedSize:     uint32(size),
			SubsegmentDuration: durations[i],
			StartsWithSAP:      true,
			SAPType:            1,
		})
	}
	return container("sidx", mustPayload(s)), nil
}
