package core

// Sample is one media sample's actual duration in 90 kHz ticks.
// VFR streams keep per-sample durations; nothing is normalized to a
// nominal frame rate.
type Sample struct {
	Duration int64
}

// SegmentInfo is the container-parser output consumed by the kernel.
type SegmentInfo struct {
	Source      string
	TrackID     uint32
	BaseTime    int64 // raw decode time in source ticks (may be wrapped)
	Samples     []Sample
	Fingerprint [32]byte // content fingerprint over sample payload
	MoofOffset  int64
	Size        int64
}

// Event is one RTP event record with its reference-clock arrival time.
type Event struct {
	ArrivalRef int64  // reference-clock ticks at arrival
	RTPTime    uint32 // raw 32-bit RTP timestamp (source clock)
	Seq        uint16
	SSRC       uint32
}
