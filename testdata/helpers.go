package testdata

import "github.com/pion/rtp"

// rtpHeader builds a minimal valid RTP header for synthetic packets.
func rtpHeader(seq uint16, ts uint32, ssrc uint32) rtp.Header {
	return rtp.Header{
		Version:        2,
		PayloadType:    96,
		SequenceNumber: seq,
		Timestamp:      ts,
		SSRC:           ssrc,
	}
}
