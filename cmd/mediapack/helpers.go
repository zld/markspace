package main

import (
	"bytes"

	"github.com/pion/rtp"
)

// readSeeker wraps a byte slice for the demuxer's io.ReadSeeker requirement.
type readSeeker struct {
	*bytes.Reader
}

func newReadSeeker(b []byte) *readSeeker {
	return &readSeeker{Reader: bytes.NewReader(b)}
}

// rtpDatagram builds one RTP packet for the boundary-verification demo.
func rtpDatagram(seq uint16, payload []byte) []byte {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96, SequenceNumber: seq, SSRC: 7,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}
