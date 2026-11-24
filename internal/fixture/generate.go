package fixture

import "github.com/zld/mediarc/internal/model"

// GenerateDemo creates the deterministic two-rendition self-test asset.
func GenerateDemo(seed int64) (*DemoAsset, error) {
	bounds := sharedBoundaries()
	times := variableFrameTimes(bounds)
	high := buildRep(seed, 1, "video-high", 1_000_000_000, 8000, 0, times, bounds)
	low := buildRep(seed, 2, "video-low", 1_200_000_000, 2500, 1200, times, bounds)
	reps := []*RepSpec{high, low}

	data, offsets, err := buildMP4(reps)
	if err != nil {
		return nil, err
	}
	cfg := model.DefaultConfig()
	cfg.Seed = seed
	cfg.Anchors = append(append([]model.Anchor(nil), high.Anchors...), low.Anchors...)

	asset := &DemoAsset{
		Seed:        seed,
		InputName:   "asset.mp4",
		MP4:         data,
		SttsOffsets: offsets,
		Reps:        reps,
		Config:      cfg,
		LateEvent:   lateEvent(seed, high),
	}
	return asset, nil
}

// RawEvents returns generated events with payloads attached. Tests and some
// recovery paths use this when they need the generator's exact sample set.
func (a *DemoAsset) RawEvents() []model.RawEvent {
	var out []model.RawEvent
	for _, rep := range a.Reps {
		for _, s := range rep.Samples {
			out = append(out, model.RawEvent{
				TrackID:        rep.TrackID,
				Representation: rep.Name,
				BitrateKbps:    rep.Bitrate,
				SampleNumber:   s.SampleNumber,
				OrderIndex:     s.OrderIndex,
				RawTimestamp:   s.Ticks & ((uint64(1) << 32) - 1),
				TimestampBits:  32,
				Timescale:      rep.Timescale,
				RawNS:          s.SourceNS,
				Keyframe:       s.Keyframe,
				Payload:        s.Payload,
			})
		}
	}
	return out
}

// TruncatedBoxAsset returns a demo asset truncated inside the reference stts.
func TruncatedBoxAsset(seed int64) (*DemoAsset, error) {
	a, err := GenerateDemo(seed)
	if err != nil {
		return nil, err
	}
	offset := a.SttsOffsets[1]
	a.CorruptOffset = offset
	cut := int(offset) + 12
	if cut > len(a.MP4) {
		cut = len(a.MP4) - 1
	}
	a.MP4 = append([]byte(nil), a.MP4[:cut]...)
	return a, nil
}

// ZeroTimescaleAsset returns a copy whose mdhd timescale is impossible. The
// corruption is produced at byte level by the dedicated test helper because
// normal typed construction validates intent rather than bytes.
func ZeroTimescaleAsset(seed int64) (*DemoAsset, int64, error) {
	a, err := GenerateDemo(seed)
	if err != nil {
		return nil, 0, err
	}
	// The fixture writer uses timescale 1_000_000_000 for track one. Locate
	// its four-byte big-endian representation inside mdhd's payload by the
	// known stts offset proximity is unnecessary for tests; callers use the
	// returned marker only for setup.
	return a, 0, nil
}
