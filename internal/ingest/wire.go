package ingest

import "avcheck/internal/media"

// WirePayloadLen is the compact capture payload size. Frames travel the
// pipeline as fixed-size identity payloads; full RGBA rasters are produced
// on demand at export time from the same seed.
const WirePayloadLen = 256

// WirePayload derives the deterministic payload for one capture identity.
func WirePayload(cfg GenConfig, track int, seq uint64) []byte {
	b := make([]byte, WirePayloadLen)
	x := uint64(uint32(cfg.Seed))<<32 ^ uint64(uint32(track))<<24 ^ seq*0x9E3779B97F4A7C15
	for i := 0; i < len(b); i++ {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b[i] = byte(x)
	}
	return b
}

// WireFrame builds one compact capture frame on demand. Ticks are
// StartTicks + track*TrackTickSkew + index*TicksPerFrame, with no random
// phase, so a verifier can regenerate it from the recorded config alone.
func WireFrame(cfg GenConfig, track, index int) (*media.Frame, error) {
	if track < 0 || track >= cfg.Tracks || index < 0 || index >= cfg.Frames {
		return nil, media.NewError(media.KindRange, uint32(track+1), uint64(index), -1,
			"wire frame index outside generated range")
	}
	if cfg.KeyInterval < 1 {
		cfg.KeyInterval = 25
	}
	seq := cfg.StartFrameSeq + uint64(index)
	payload := WirePayload(cfg, track, seq)
	f := &media.Frame{
		StreamID: uint32(track + 1),
		Seq:      seq,
		Ticks:    cfg.StartTicks + int64(track)*cfg.TrackTickSkew + int64(index)*cfg.TicksPerFrame,
		Key:      index%cfg.KeyInterval == 0,
		Meta:     media.Meta{Version: media.MetaVersion, Length: uint32(len(payload))},
		Payload:  payload,
	}
	f.Sum = f.ComputeSum()
	return f, nil
}
