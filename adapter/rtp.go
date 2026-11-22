package adapter

// RTPAdapter parses RTP packet streams through pion/rtp. The concrete stream
// pusher is implemented in rtp_parse.go; this file holds the descriptor.
type RTPAdapter struct {
	baseAdapter
}

// RTPAdapterName is the registry key for the RTP parser family.
const RTPAdapterName = "rtp"

// RTPAdapterV1 is the first (and current) parser version.
const RTPAdapterV1 uint32 = 1

// NewRTPAdapterV1 builds the version-1 RTP adapter descriptor.
func NewRTPAdapterV1() *RTPAdapter {
	return &RTPAdapter{baseAdapter{
		name: RTPAdapterName, ver: RTPAdapterV1,
		caps: map[Capability]struct{}{
			CapRTPWrap: {},
			CapReorder: {},
		},
	}}
}
