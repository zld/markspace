package demux

import (
	"encoding/binary"

	"mediapipeline/internal/track"
)

func parsePES(buf *pesBuffer) (tsPES, bool) {
	d := buf.data
	if len(d) < 9 || d[0] != 0 || d[1] != 0 || d[2] != 1 {
		return tsPES{}, false
	}
	headerLen := int(d[8])
	if 9+headerLen > len(d) {
		return tsPES{}, false
	}
	flags := d[7]
	payload := d[9+headerLen:]
	if declared := int(binary.BigEndian.Uint16(d[4:6])); declared != 0 {
		// PES_packet_length counts the optional header plus payload.  Trim any
		// transport stuffing that followed the declared packet.
		want := declared - 3 - headerLen
		if want < 0 {
			return tsPES{}, false
		}
		if want < len(payload) {
			payload = payload[:want]
		}
	}
	pes := tsPES{pid: buf.pid, offset: buf.first, payload: payload}
	if flags&0x80 != 0 && headerLen >= 5 {
		pes.pts = readPTS(d[9:14])
		pes.hasPTS = true
	}
	if flags&0x40 != 0 && headerLen >= 10 {
		pes.dts = readPTS(d[14:19])
		pes.hasDTS = true
	}
	return pes, true
}

func readPTS(b []byte) uint64 {
	v := uint64((b[0]>>1)&0x07) << 30
	v |= uint64(b[1]) << 22
	v |= uint64(b[2]>>1) << 15
	v |= uint64(b[3]) << 7
	v |= uint64(b[4] >> 1)
	return v
}

func tsHandler(k track.Kind) string {
	switch k {
	case track.KindVideo:
		return "vide"
	case track.KindAudio:
		return "soun"
	case track.KindSubtitle:
		return "subt"
	case track.KindChapter:
		return "text"
	case track.KindMetadata:
		return "meta"
	}
	return ""
}

func sortUint16(v []uint16) {
	for i := 0; i < len(v); i++ {
		for j := i + 1; j < len(v); j++ {
			if v[j] < v[i] {
				v[i], v[j] = v[j], v[i]
			}
		}
	}
}

type annexBNAL struct {
	off  int
	data []byte
}
