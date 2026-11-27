package main

import (
	"encoding/hex"

	"github.com/example/mediad/internal/pipeline"
	"github.com/example/mediad/internal/rtp"
)

// orderInput runs RTP ordering solely to compute the input content digest,
// matching what the pipeline uses inside its cache key.
func orderInput(in pipeline.Input) (*rtp.Result, error) {
	type dg = struct {
		Data   []byte
		Offset int64
	}
	dgs := make([]dg, len(in.Datagrams))
	copy(dgs, in.Datagrams)
	return rtp.Order(dgs)
}

// rtpDigestHex renders the accepted-packet digest as hex.
func rtpDigestHex(order *rtp.Result) string {
	sum := rtp.PayloadDigest(order.Packets)
	return hex.EncodeToString(sum[:])
}
