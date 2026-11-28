package media

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"mediad/timeline"
)

// rtpCaptureMagic prefixes the synthetic capture format used by fixtures and
// tests. The format is deliberately trivial and self-describing: a magic, a
// 32-bit packet count, then per packet a 32-bit length followed by the raw RTP
// bytes. Real captures would use a pcap reader here; the packet-level logic
// below is identical either way.
var rtpCaptureMagic = []byte("MRTP")

// ParseRTPCapture reads a capture produced by fixture.EncodeRTPCapture and
// returns packets in arrival order. A packet whose declared length overruns the
// buffer, or whose RTP header fails to parse, is a located error naming the byte
// offset; the capture is refused rather than partially trusted.
func ParseRTPCapture(path string, data []byte) ([]RTPPacket, error) {
	if len(data) < 8 || string(data[:4]) != string(rtpCaptureMagic) {
		return nil, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeUnsupported, File: path,
			Message: "not an RTP capture (bad magic)"}}
	}
	count := int(binary.BigEndian.Uint32(data[4:8]))
	pos := 8
	packets := make([]RTPPacket, 0, count)
	for i := 0; i < count; i++ {
		if pos+4 > len(data) {
			return nil, &timeline.LocatedError{Issue: timeline.Issue{
				Severity: timeline.SeverityError, Code: timeline.CodeTruncated, File: path, ByteOffset: int64(pos),
				Message: fmt.Sprintf("packet %d length header runs past capture end", i)}}
		}
		n := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		start := pos + 4
		if n < 12 || start+n > len(data) {
			return nil, &timeline.LocatedError{Issue: timeline.Issue{
				Severity: timeline.SeverityError, Code: timeline.CodeTruncated, File: path, ByteOffset: int64(pos),
				Message: fmt.Sprintf("packet %d declares %d bytes, capture holds %d", i, n, len(data)-start)}}
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(data[start : start+n]); err != nil {
			return nil, &timeline.LocatedError{Issue: timeline.Issue{
				Severity: timeline.SeverityError, Code: timeline.CodeInvalidLength, File: path, ByteOffset: int64(start),
				Message: fmt.Sprintf("packet %d: %v", i, err)}}
		}
		packets = append(packets, RTPPacket{Packet: &pkt, Arrival: i, SrcOffset: int64(start)})
		pos = start + n
	}
	return packets, nil
}
