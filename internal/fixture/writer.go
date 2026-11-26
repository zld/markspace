package fixture

import (
	"encoding/binary"
	"math"

	"assetcheck/internal/analyze"
	"assetcheck/internal/ebml"
)

func element(id uint32, payload []byte) []byte {
	size, err := ebml.EncodeSize(uint64(len(payload)))
	if err != nil {
		panic(err)
	}
	out := append([]byte(nil), ebml.EncodeID(id)...)
	out = append(out, size...)
	out = append(out, payload...)
	return out
}

func uintElement(id uint32, value uint64) []byte {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], value)
	n := 8
	for n > 1 && raw[8-n] == 0 {
		n--
	}
	return element(id, raw[8-n:])
}

func stringElement(id uint32, value string) []byte {
	return element(id, []byte(value))
}

func floatElement(id uint32, value float64) []byte {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], math.Float64bits(value))
	return element(id, raw[:])
}

type TrackConfig struct {
	Number   uint64
	Type     uint64
	Language string
	CodecID  string
	Channels uint64
	Sampling float64
	BitDepth uint64
}

func trackElement(cfg TrackConfig) []byte {
	var payload []byte
	payload = append(payload, uintElement(ebml.IDTrackNumber, cfg.Number)...)
	payload = append(payload, uintElement(ebml.IDTrackType, cfg.Type)...)
	if cfg.Language != "" {
		payload = append(payload, stringElement(ebml.IDLanguage, cfg.Language)...)
	}
	payload = append(payload, stringElement(ebml.IDCodecID, cfg.CodecID)...)
	if cfg.Channels != 0 {
		payload = append(payload, uintElement(ebml.IDChannels, cfg.Channels)...)
	}
	if cfg.BitDepth != 0 {
		payload = append(payload, uintElement(ebml.IDBitDepth, cfg.BitDepth)...)
	}
	if cfg.Sampling != 0 {
		payload = append(payload, floatElement(ebml.IDSamplingFreq, cfg.Sampling)...)
	}
	return element(ebml.IDTrackEntry, payload)
}

func simpleBlock(trackNumber uint64, relativeTimecodeMS int16, flags byte, payload []byte) []byte {
	trackVINT, err := ebml.EncodeSize(trackNumber)
	if err != nil {
		panic(err)
	}
	block := append([]byte(nil), trackVINT...)
	var tc [2]byte
	binary.BigEndian.PutUint16(tc[:], uint16(relativeTimecodeMS))
	block = append(block, tc[:]...)
	block = append(block, flags)
	block = append(block, payload...)
	return element(ebml.IDSimpleBlock, block)
}

func pcmBlock(sampleOffset, samples int64) []byte {
	b := make([]byte, samples*2)
	for i := int64(0); i < samples; i++ {
		v := analyze.ReferenceSample(sampleOffset + i)
		b[2*i] = byte(uint16(v))
		b[2*i+1] = byte(uint16(v) >> 8)
	}
	return b
}
