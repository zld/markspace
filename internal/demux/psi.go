package demux

import (
	"encoding/binary"

	"mediapipeline/internal/fault"
	"mediapipeline/internal/track"
)

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
