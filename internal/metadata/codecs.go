package metadata

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// DecodeID3 decodes the generated ID3v2.3 subset: a 10-byte header followed by
// TIT2/TXXX frames. Unknown frames are skipped, never interpreted. The first
// human-readable text becomes the event text; the returned rest is the raw
// frame bytes for the manifest digest.
func DecodeID3(payload []byte) (text string, rest []byte, ok bool) {
	if len(payload) < 10 || string(payload[:3]) != "ID3" {
		return "", nil, false
	}
	if payload[3] != 3 {
		return "", nil, false // only ID3v2.3 is generated
	}
	flags := payload[5]
	if flags&0x80 != 0 {
		return "", nil, false // unsynchronised form is not generated
	}
	// The size field is a 28-bit syncsafe integer, excluding the header.
	size := int(payload[6]&0x7f)<<21 | int(payload[7]&0x7f)<<14 | int(payload[8]&0x7f)<<7 | int(payload[9]&0x7f)
	if size < 0 || 10+size > len(payload) {
		return "", nil, false
	}
	body := payload[10 : 10+size]
	var parts []string
	for off := 0; off+10 <= len(body); {
		id := string(body[off : off+4])
		if id == "" || id[0] < 'A' || id[0] > 'Z' {
			break // padding reached
		}
		fsize := int(binary.BigEndian.Uint32(body[off+4 : off+8]))
		if fsize < 0 || off+10+fsize > len(body) {
			return "", nil, false
		}
		if id == "TIT2" || id == "TXXX" {
			if t := decodeTextFrame(body[off+10 : off+10+fsize]); t != "" {
				parts = append(parts, t)
			}
		}
		off += 10 + fsize
	}
	return strings.Join(parts, "; "), append([]byte(nil), body...), true
}

func decodeTextFrame(frame []byte) string {
	if len(frame) < 1 {
		return ""
	}
	// Encoding byte: 0 = ISO-8859-1, 1 = UTF-16, 3 = UTF-8.
	switch frame[0] {
	case 0:
		return strings.TrimRight(string(frame[1:]), "\x00")
	case 3:
		return strings.TrimRight(string(frame[1:]), "\x00")
	case 1:
		var b strings.Builder
		for i := 1; i+1 < len(frame); i += 2 {
			v := binary.BigEndian.Uint16(frame[i : i+2])
			if v == 0 {
				break
			}
			b.WriteByte(byte(v)) // generated UTF-16 content is ASCII
		}
		return b.String()
	default:
		return ""
	}
}

// DecodeSEI decodes the generated SEI payload: a NAL unit header, the
// user_data_unregistered payload type (5) with a 16-byte UUID, followed by a
// NUL-terminated message and the raw codec tail. It validates sizes so a
// damaged payload is reported instead of guessed at.
func DecodeSEI(payload []byte) (text string, rest []byte, ok bool) {
	// NAL header(1) + payload type(1) + size(1) + UUID(16) + at least one
	// message byte is the minimum useful SEI payload.
	if len(payload) < 20 {
		return "", nil, false
	}
	nalType := payload[0] & 0x1f
	if nalType != 6 { // SEI NAL unit
		return "", nil, false
	}
	payloadType := payload[1]
	if payloadType != 5 { // user_data_unregistered
		return "", nil, false
	}
	// payloadSize is expressed in bytes, escaping 0xFF is not generated.
	size := int(payload[2])
	if size < 16 || 3+size > len(payload) {
		return "", nil, false
	}
	uuid := payload[3:19]
	msg := payload[19 : 3+size]
	term := 0
	for term < len(msg) && msg[term] != 0 {
		term++
	}
	return uuidString(uuid) + ":" + string(msg[:term]), append([]byte(nil), payload[3+size:]...), true
}

func uuidString(u []byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(u[0:4]), u[4:6], u[6:8], u[8:10], u[10:16])
}
