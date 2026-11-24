package codec

import "example.com/media-kernel/media"

// ADTSHeader will expose duration-relevant ADTS fields.
type ADTSHeader struct {
	Info
}

// ParseADTSHeader is completed in the next incremental batch.
func ParseADTSHeader(packet []byte) (*ADTSHeader, *media.Error) {
	return nil, media.NewError("codec.aac", media.CodeUnsupported, "ADTS parser skeleton", nil)
}

// ParseAAC is completed in the next incremental batch.
func ParseAAC(packet []byte) (*Info, *media.Error) {
	return nil, media.NewError("codec.aac", media.CodeUnsupported, "AAC parser skeleton", nil)
}

// BuildADTSFrame is completed in the next incremental batch.
func BuildADTSFrame(sampleRateIndex byte, channels byte, blocksMinusOne byte, payloadSize int) []byte {
	return nil
}
