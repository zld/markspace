package scan

import (
	"github.com/pion/rtp"

	"mediad/internal/model"
)

// handleH264 processes RFC 6184 payloads: single NAL, STAP-A(24) and FU-A(28).
func (x *RTPSEIExtractor) handleH264(pkt *rtp.Packet, off int64) error {
	payload := pkt.Payload
	if len(payload) == 0 {
		return nil
	}
	nalType := payload[0] & 0x1F
	switch {
	case nalType >= 1 && nalType <= 23: // single NAL unit
		x.deliverH264NAL(payload, pkt.Timestamp, off)
	case nalType == 24: // STAP-A
		x.handleSTAPA(payload, pkt.Timestamp, off)
	case nalType == 28: // FU-A
		x.handleFUA(payload, pkt.Timestamp, off)
	default:
		x.frag = nil // MTAP/other modes not produced by our fixtures
	}
	return nil
}

// deliverH264NAL emits an SEI event when the NAL unit is an SEI (type 6).
func (x *RTPSEIExtractor) deliverH264NAL(nal []byte, ts uint32, off int64) {
	if len(nal) == 0 {
		return
	}
	if nal[0]&0x1F != 6 {
		return
	}
	if _, ok := parseSEIMessages(nal, 1); !ok {
		x.Anomalies = append(x.Anomalies, model.Anomaly{
			Type: model.AnomalyUnparseable, TrackID: x.trackID,
			Message: "malformed H264 SEI", SourceOffset: off, TimeBase: x.base,
		})
		return
	}
	x.emitSEI(nal, ts, off)
}

func (x *RTPSEIExtractor) handleSTAPA(payload []byte, ts uint32, off int64) {
	i := 1 // skip STAP-A header
	for i+2 <= len(payload) {
		size := int(uint16(payload[i])<<8 | uint16(payload[i+1]))
		i += 2
		if i+size > len(payload) {
			x.abandon(off, "truncated STAP-A")
			return
		}
		x.deliverH264NAL(payload[i:i+size], ts, off+int64(i))
		i += size
	}
}

func (x *RTPSEIExtractor) handleFUA(payload []byte, ts uint32, off int64) {
	if len(payload) < 2 {
		x.abandon(off, "short FU-A")
		return
	}
	fuHeader := payload[1]
	start := fuHeader&0x80 != 0
	end := fuHeader&0x40 != 0
	nalType := fuHeader & 0x1F
	if start {
		// Reconstruct the original NAL header: forbidden/type from indicator.
		header := []byte{(payload[0] & 0xE0) | nalType}
		x.frag = &fragment{
			nalType: nalType,
			buf:     append(header, payload[2:]...),
			ts:      ts, off: off,
		}
	} else {
		if x.frag == nil || x.frag.nalType != nalType {
			x.abandon(off, "FU-A continuation without start")
			return
		}
		if len(x.frag.buf)+len(payload)-2 > maxNALUSize {
			x.abandon(off, "reassembled NAL exceeds size guard")
			return
		}
		x.frag.buf = append(x.frag.buf, payload[2:]...)
	}
	if end {
		f := x.frag
		x.frag = nil
		x.deliverH264NAL(f.buf, f.ts, f.off)
	}
}

// handleH265 processes RFC 7798 payloads: single NAL, AP(48) and FU(49).
func (x *RTPSEIExtractor) handleH265(pkt *rtp.Packet, off int64) error {
	payload := pkt.Payload
	if len(payload) < 2 {
		return nil
	}
	nalType := (payload[0] >> 1) & 0x3F
	switch {
	case nalType == 39 || nalType == 40: // single SEI NAL unit
		x.deliverH265NAL(payload, pkt.Timestamp, off)
	case nalType == 48: // aggregation packet
		x.handleAP(payload, pkt.Timestamp, off)
	case nalType == 49: // fragmentation unit
		x.handleFU(payload, pkt.Timestamp, off)
	default:
		x.frag = nil
	}
	return nil
}

func (x *RTPSEIExtractor) deliverH265NAL(nal []byte, ts uint32, off int64) {
	if len(nal) < 2 {
		return
	}
	t := (nal[0] >> 1) & 0x3F
	if t != 39 && t != 40 {
		return
	}
	// H.265 NAL header is two bytes; SEI RBSP follows.
	if _, ok := parseSEIMessages(nal, 2); !ok {
		x.Anomalies = append(x.Anomalies, model.Anomaly{
			Type: model.AnomalyUnparseable, TrackID: x.trackID,
			Message: "malformed H265 SEI", SourceOffset: off, TimeBase: x.base,
		})
		return
	}
	x.emitSEI(nal, ts, off)
}

func (x *RTPSEIExtractor) handleAP(payload []byte, ts uint32, off int64) {
	i := 2 // skip 2-byte AP header
	for i+2 <= len(payload) {
		size := int(u16be(payload[i : i+2]))
		i += 2
		if i+size > len(payload) {
			x.abandon(off, "truncated H265 AP")
			return
		}
		x.deliverH265NAL(payload[i:i+size], ts, off+int64(i))
		i += size
	}
}

func (x *RTPSEIExtractor) handleFU(payload []byte, ts uint32, off int64) {
	if len(payload) < 3 {
		x.abandon(off, "short H265 FU")
		return
	}
	fuHeader := payload[2]
	start := fuHeader&0x80 != 0
	end := fuHeader&0x40 != 0
	nalType := fuHeader & 0x3F
	if start {
		// Reconstruct the 2-byte H.265 NAL header from the FU indicator.
		header := []byte{payload[0], (payload[1] & 0xF9) | (nalType << 1)}
		x.frag = &fragment{
			nalType: nalType,
			buf:     append(header, payload[3:]...),
			ts:      ts, off: off,
		}
	} else {
		if x.frag == nil || x.frag.nalType != nalType {
			x.abandon(off, "FU continuation without start")
			return
		}
		if len(x.frag.buf)+len(payload)-3 > maxNALUSize {
			x.abandon(off, "reassembled NAL exceeds size guard")
			return
		}
		x.frag.buf = append(x.frag.buf, payload[3:]...)
	}
	if end {
		f := x.frag
		x.frag = nil
		x.deliverH265NAL(f.buf, f.ts, f.off)
	}
}
