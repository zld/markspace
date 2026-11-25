package demo

import (
	"path/filepath"

	"github.com/pion/rtp"

	"mediascan/internal/rtpcheck"
)

// generateRTP writes a length-framed RTP stream whose timestamps cross a
// 2^32 wrap boundary, exercising the unwrap and boundary checks.
func (b *Bundle) generateRTP() error {
	// 90 kHz clock; the second packet wraps past the 32-bit cycle.
	headers := []struct {
		seq uint16
		ts  uint32
	}{
		{0, 0xFFFFFFF0},
		{1, 0x00000005},
		{2, 0x0000000F},
		{3, 0x00000019},
	}
	var framed []byte
	for _, h := range headers {
		p := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: h.seq,
				Timestamp:      h.ts,
				SSRC:           99,
				Marker:         h.seq == uint16(len(headers)-1),
			},
			Payload: []byte{0xDE, 0xAD, 0xBE, 0xEF},
		}
		raw, err := rtpcheck.EncodeDatagram(&p)
		if err != nil {
			return err
		}
		framed = append(framed, raw...)
	}
	path := filepath.Join(b.Root, DirRTP, "stream.rtp")
	if err := writeFile(path, framed); err != nil {
		return err
	}
	b.RTPPath = path
	return nil
}
