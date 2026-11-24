package demux

import (
	"encoding/binary"

	"mediapipeline/internal/fault"
)

// scanTS performs the validating first pass.  It records cut ranges in media
// when the final packet is incomplete and tolerant mode is on.
func scanTS(data []byte, media *Media, opts Options) (*tsPSI, error) {
	psi := &tsPSI{pmtPIDs: make(map[uint16]bool), streams: make(map[uint16]tsStream)}
	cc := make(map[uint16]uint8)
	pos := 0
	for pos < len(data) {
		if pos+tsPacketSize > len(data) {
			if opts.TolerateTruncated {
				addCut(media, Range{Start: int64(pos), End: int64(len(data))})
				break
			}
			return nil, fault.New(fault.StageDemux, "ts", fault.OpTruncated, int64(len(data)),
				"incomplete transport packet at end of input")
		}
		if data[pos] != tsSyncByte {
			return nil, fault.New(fault.StageDemux, "ts", fault.OpPacketHeader, int64(pos),
				"transport packet does not start with sync byte")
		}
		pkt := data[pos : pos+tsPacketSize]
		pid := binary.BigEndian.Uint16(pkt[1:]) & 0x1fff
		afc := (pkt[3] >> 4) & 3
		hasPayload := afc&1 != 0
		payloadStart := 4
		if afc&2 != 0 {
			afLen := int(pkt[4])
			payloadStart = 5 + afLen
			if payloadStart > tsPacketSize {
				return nil, fault.New(fault.StageDemux, "ts", fault.OpPacketHeader, int64(pos+4),
					"adaptation field length exceeds transport packet")
			}
		}
		if !hasPayload {
			pos += tsPacketSize
			continue
		}
		payload := pkt[payloadStart:]
		if len(payload) == 0 {
			pos += tsPacketSize
			continue
		}
		counter := pkt[3] & 0x0f
		if prev, ok := cc[pid]; ok && (prev+1)&0x0f != counter {
			return nil, fault.New(fault.StageDemux, "ts", fault.OpSequence, int64(pos),
				"continuity counter discontinuity on elementary stream")
		}
		cc[pid] = counter
		pusi := pkt[1]&0x40 != 0
		if pid == 0 || pid == psi.pmtPID || psi.pmtPIDs[pid] {
			if pusi && len(payload) > int(payload[0]) {
				section := payload[1+int(payload[0]):]
				off := int64(pos+payloadStart+1) + int64(payload[0])
				if err := parsePSISection(psi, pid, section, off); err != nil {
					return nil, err
				}
			}
		}
		pos += tsPacketSize
	}
	return psi, nil
}
