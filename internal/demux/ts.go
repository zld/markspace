package demux

import (
	"encoding/binary"

	"mediapipeline/internal/fault"
	"mediapipeline/internal/track"
)

const (
	tsPacketSize = 188
	tsSyncByte   = 0x47
	tsTimescale  = 90000
)

const (
	tsStreamH264 = 0x1b
	tsStreamHEVC = 0x24
	tsStreamAAC  = 0x0f
	tsStreamID3  = 0x15
	tsStreamPriv = 0x06
)

// ParseTS demuxes an MPEG-TS byte slice.  It works in two passes so the
// elementary stream map is final before any PES is interpreted: pass one
// validates packet framing, continuity and PSI; pass two reassembles PES
// packets at their exact source offsets.
func ParseTS(data []byte, opts Options) (*Media, error) {
	media := &Media{Format: "ts"}
	psi, err := scanTS(data, media, opts)
	if err != nil {
		return nil, err
	}
	if psi.pmtPID == 0 {
		return nil, fault.New(fault.StageDemux, "ts", fault.OpIndexIncomplete, 0,
			"stream has no PAT/PMT program information")
	}
	if len(psi.streams) == 0 {
		return nil, fault.New(fault.StageDemux, "ts", fault.OpIndexIncomplete, 0,
			"PMT declares no elementary streams")
	}
	tracks, events, err := assemblePES(data, psi, media)
	if err != nil {
		return nil, err
	}
	media.Tracks = tracks
	media.Events = events
	for i := range media.Events {
		media.Events[i].Seq = i
	}
	return media, nil
}

type tsStream struct {
	pid   uint16
	typ   byte
	kind  track.Kind
	codec string
}

type tsPSI struct {
	pmtPID  uint16
	pmtPIDs map[uint16]bool
	streams map[uint16]tsStream
}

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

func parsePSISection(psi *tsPSI, pid uint16, section []byte, off int64) error {
	if len(section) < 3 || section[0] == 0xff {
		return nil // stuffing
	}
	tableID := section[0]
	sectLen := int(binary.BigEndian.Uint16(section[1:]) & 0x0fff)
	if 3+sectLen > len(section) {
		return fault.New(fault.StageDemux, "ts", fault.OpIncompleteHeader, off+int64(len(section)),
			"PSI section extends past transport packet")
	}
	body := section[3 : 3+sectLen]
	switch tableID {
	case 0x00: // PAT
		if len(body) < 5 {
			return fault.New(fault.StageDemux, "ts", fault.OpIncompleteHeader, off+int64(len(section)), "PAT section truncated")
		}
		p := 5
		for p+4 <= len(body)-4 {
			program := binary.BigEndian.Uint16(body[p:])
			pmtPID := binary.BigEndian.Uint16(body[p+2:]) & 0x1fff
			p += 4
			if program == 0 {
				continue
			}
			if psi.pmtPID == 0 {
				psi.pmtPID = pmtPID
			}
			psi.pmtPIDs[pmtPID] = true
		}
	case 0x02: // PMT
		if len(body) < 9 {
			return fault.New(fault.StageDemux, "ts", fault.OpIncompleteHeader, off+int64(len(section)), "PMT section truncated")
		}
		// body = extension(2) version(1) section(1) last_section(1)
		// program_info_length(2) then elementary stream entries.
		progInfoLen := int(binary.BigEndian.Uint16(body[5:]) & 0x0fff)
		p := 7 + progInfoLen
		for p+5 <= len(body)-4 {
			streamType := body[p]
			esPID := binary.BigEndian.Uint16(body[p+1:]) & 0x1fff
			esInfoLen := int(binary.BigEndian.Uint16(body[p+3:]) & 0x0fff)
			p += 5
			if p+esInfoLen > len(body) {
				return fault.New(fault.StageDemux, "ts", fault.OpIncompleteHeader, off+int64(len(section)),
					"PMT elementary stream descriptors truncated")
			}
			desc := body[p : p+esInfoLen]
			p += esInfoLen
			st := tsStream{pid: esPID, typ: streamType}
			st.kind, st.codec = classifyTSStream(streamType, desc)
			psi.streams[esPID] = st
		}
	}
	return nil
}

func classifyTSStream(streamType byte, desc []byte) (track.Kind, string) {
	switch streamType {
	case tsStreamH264, 0x01, 0x02:
		return track.KindVideo, "avc"
	case tsStreamHEVC:
		return track.KindVideo, "hevc"
	case tsStreamAAC, 0x03, 0x04:
		return track.KindAudio, "aac"
	case tsStreamID3:
		return track.KindMetadata, "id3"
	case tsStreamPriv:
		if hasDescriptor(desc, 0x59) || hasDescriptor(desc, 0x46) {
			return track.KindSubtitle, "subtitle"
		}
		return track.KindMetadata, "private"
	}
	return track.KindUnknown, "unknown"
}

func hasDescriptor(desc []byte, tag byte) bool {
	for p := 0; p+2 <= len(desc); {
		t := desc[p]
		l := int(desc[p+1])
		p += 2
		if p+l > len(desc) {
			return false
		}
		if t == tag {
			return true
		}
		p += l
	}
	return false
}
