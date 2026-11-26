package fixture

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"github.com/pion/rtp"
)

// TrackSpec describes the tiny synthetic RTP-in-MP4 track written by fixtures.
type TrackSpec struct {
	FileName            string
	HandlerType         string // "vide" or "soun"
	Timescale           uint32
	MovieTimescale      uint32
	SampleDuration      uint32
	RTPTimestamps       []uint32
	SyncSamples         []uint32
	EmptyEditMovieTicks uint32
	MediaEditMovieTicks uint32
	OversizeSample      int
	OversizeBytes       uint32
	Seed                uint32
}

// WriteTrackMP4 writes a deterministic one-track MP4 file.
func WriteTrackMP4(dir string, spec TrackSpec) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, spec.FileName)
	_, sizes, mdatPayload, err := buildPackets(spec)
	if err != nil {
		return "", err
	}

	ftyp := buildFtyp()
	moovZero := buildMoov(spec, sizes, 0)
	dataOffset := uint32(len(ftyp) + len(moovZero) + 8)
	moov := buildMoov(spec, sizes, dataOffset)
	mdat := box("mdat", mdatPayload)

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	for _, b := range [][]byte{ftyp, moov, mdat} {
		if _, err := f.Write(b); err != nil {
			return "", err
		}
	}
	return path, nil
}

func buildPackets(spec TrackSpec) ([][]byte, []uint32, []byte, error) {
	packets := make([][]byte, 0, len(spec.RTPTimestamps))
	sizes := make([]uint32, 0, len(spec.RTPTimestamps))
	var payload []byte
	for i, ts := range spec.RTPTimestamps {
		b, err := (&rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96, SequenceNumber: uint16(i + 1),
				Timestamp: ts, SSRC: 0x5eed0000 ^ (spec.Seed * 2654435761),
			},
			Payload: []byte{byte(i + 1), 0x7d, 0x00, 0x04},
		}).Marshal()
		if err != nil {
			return nil, nil, nil, err
		}
		packets = append(packets, b)
		size := uint32(len(b))
		if spec.OversizeSample == i+1 {
			size = spec.OversizeBytes
		}
		sizes = append(sizes, size)
		payload = append(payload, b...)
	}
	return packets, sizes, payload, nil
}

func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func fullBox(version byte, flags uint32, payload []byte) []byte {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return append(head, payload...)
}

func buildFtyp() []byte {
	p := make([]byte, 0, 20)
	p = binary.BigEndian.AppendUint32(p, fourCC("isom"))
	p = binary.BigEndian.AppendUint32(p, 0x00000200)
	p = append(p, []byte("isomiso2mp41")...)
	return box("ftyp", p)
}

func buildMoov(spec TrackSpec, sizes []uint32, chunkOffset uint32) []byte {
	movieTS := spec.MovieTimescale
	if movieTS == 0 {
		movieTS = 1000
	}
	count := uint32(len(spec.RTPTimestamps))
	mdhd := buildMdhd(spec.Timescale, spec.SampleDuration*count)
	hdlr := buildHdlr(spec.HandlerType)
	stbl := box("stbl", buildStbl(spec, sizes, chunkOffset))
	mdia := box("mdia", append(append(mdhd, hdlr...), box("minf", stbl)...))
	trakPayload := mdia
	if elst := buildElst(spec, movieTS); elst != nil {
		trakPayload = append(elst, mdia...)
	}
	trak := box("trak", trakPayload)
	mvhd := buildMvhd(movieTS, spec.MediaEditMovieTicks)
	return box("moov", append(mvhd, trak...))
}

func buildMdhd(timescale, duration uint32) []byte {
	p := make([]byte, 0, 20)
	p = binary.BigEndian.AppendUint32(p, 0) // creation
	p = binary.BigEndian.AppendUint32(p, 0) // modification
	p = binary.BigEndian.AppendUint32(p, timescale)
	p = binary.BigEndian.AppendUint32(p, duration)
	p = binary.BigEndian.AppendUint16(p, 0) // language
	p = binary.BigEndian.AppendUint16(p, 0) // pre-defined
	return box("mdhd", fullBox(0, 0, p))
}

func buildMvhd(timescale, duration uint32) []byte {
	p := make([]byte, 0, 96)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, timescale)
	p = binary.BigEndian.AppendUint32(p, duration)
	p = binary.BigEndian.AppendUint32(p, 0x00010000) // rate 1.0
	p = binary.BigEndian.AppendUint16(p, 0x0100)     // volume 1.0
	p = binary.BigEndian.AppendUint16(p, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	matrix := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	for _, v := range matrix {
		p = binary.BigEndian.AppendUint32(p, v)
	}
	for i := 0; i < 6; i++ {
		p = binary.BigEndian.AppendUint32(p, 0)
	}
	p = binary.BigEndian.AppendUint32(p, 2) // next track id
	return box("mvhd", fullBox(0, 0, p))
}

func buildHdlr(handler string) []byte {
	p := make([]byte, 0, 32)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = append(p, handler...)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = append(p, []byte("medpipe\x00")...)
	return box("hdlr", fullBox(0, 0, p))
}

func buildElst(spec TrackSpec, movieTS uint32) []byte {
	if spec.EmptyEditMovieTicks == 0 {
		return nil
	}
	p := make([]byte, 0, 24)
	p = binary.BigEndian.AppendUint32(p, 2) // entry count
	p = binary.BigEndian.AppendUint32(p, spec.EmptyEditMovieTicks)
	p = binary.BigEndian.AppendUint32(p, uint32(0xFFFFFFFF)) // media_time = -1
	p = binary.BigEndian.AppendUint16(p, 1)
	p = binary.BigEndian.AppendUint16(p, 0)
	mediaDur := spec.MediaEditMovieTicks
	if mediaDur == 0 {
		mediaDur = uint32(uint64(spec.SampleDuration) * uint64(len(spec.RTPTimestamps)) * uint64(movieTS) / uint64(spec.Timescale))
	}
	p = binary.BigEndian.AppendUint32(p, mediaDur)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint16(p, 1)
	p = binary.BigEndian.AppendUint16(p, 0)
	elst := box("elst", fullBox(0, 0, p))
	return box("edts", elst)
}

func buildStbl(spec TrackSpec, sizes []uint32, chunkOffset uint32) []byte {
	stsd := box("stsd", fullBox(0, 0, binary.BigEndian.AppendUint32(nil, 0)))
	sttsPayload := binary.BigEndian.AppendUint32(nil, 1)
	sttsPayload = binary.BigEndian.AppendUint32(sttsPayload, uint32(len(sizes)))
	sttsPayload = binary.BigEndian.AppendUint32(sttsPayload, spec.SampleDuration)
	stts := box("stts", fullBox(0, 0, sttsPayload))

	var stss []byte
	if len(spec.SyncSamples) > 0 {
		p := binary.BigEndian.AppendUint32(nil, uint32(len(spec.SyncSamples)))
		for _, n := range spec.SyncSamples {
			p = binary.BigEndian.AppendUint32(p, n)
		}
		stss = box("stss", fullBox(0, 0, p))
	}

	stscPayload := binary.BigEndian.AppendUint32(nil, 1)
	stscPayload = binary.BigEndian.AppendUint32(stscPayload, 1)
	stscPayload = binary.BigEndian.AppendUint32(stscPayload, uint32(len(sizes)))
	stscPayload = binary.BigEndian.AppendUint32(stscPayload, 1)
	stsc := box("stsc", fullBox(0, 0, stscPayload))

	stszPayload := binary.BigEndian.AppendUint32(nil, 0)
	stszPayload = binary.BigEndian.AppendUint32(stszPayload, uint32(len(sizes)))
	for _, sz := range sizes {
		stszPayload = binary.BigEndian.AppendUint32(stszPayload, sz)
	}
	stsz := box("stsz", fullBox(0, 0, stszPayload))

	stcoPayload := binary.BigEndian.AppendUint32(nil, 1)
	stcoPayload = binary.BigEndian.AppendUint32(stcoPayload, chunkOffset)
	stco := box("stco", fullBox(0, 0, stcoPayload))

	out := append([]byte{}, stsd...)
	out = append(out, stts...)
	if stss != nil {
		out = append(out, stss...)
	}
	out = append(out, stsc...)
	out = append(out, stsz...)
	out = append(out, stco...)
	return out
}

func fourCC(s string) uint32 {
	return uint32(s[0])<<24 | uint32(s[1])<<16 | uint32(s[2])<<8 | uint32(s[3])
}
