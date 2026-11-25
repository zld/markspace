package rtp

// Payload record layout of the generated archive RTP stream: version(1)
// trackID(1) kind(1) reserved(1) presentation(8) duration(4) text(NUL) data.
// It is defined here, next to the reorderer, because the reorderer is the only
// subsystem that turns wire payloads into timeline events.

const (
	// PayloadVersion is the only accepted generated payload version.
	PayloadVersion = 1
	// PayloadHeaderLen is the fixed prefix length of a payload record.
	PayloadHeaderLen = 16
)

// PayloadKind is the semantic kind code carried by a generated RTP payload.
type PayloadKind byte

const (
	PayloadSubtitle PayloadKind = 1
	PayloadChapter  PayloadKind = 2
	PayloadID3      PayloadKind = 3
	PayloadSEI      PayloadKind = 4
)

// Payload is a decoded generated RTP payload record.
type Payload struct {
	TrackID      uint32
	Kind         PayloadKind
	Presentation uint64
	Duration     uint32
	Text         string
	Data         []byte
}

// DecodePayload decodes a generated RTP payload. ok is false for payloads that
// do not carry the expected layout; the caller must report these as malformed
// rather than guess at their contents.
func DecodePayload(payload []byte) (Payload, bool) {
	if len(payload) < PayloadHeaderLen || payload[0] != PayloadVersion {
		return Payload{}, false
	}
	p := Payload{
		TrackID:      uint32(payload[1]),
		Kind:         PayloadKind(payload[2]),
		Presentation: binaryBE64(payload[4:12]),
		Duration:     uint32(binaryBE64(payload[12:16])),
	}
	rest := payload[PayloadHeaderLen:]
	if idx := indexByte(rest, 0); idx >= 0 {
		p.Text = string(rest[:idx])
		p.Data = append([]byte(nil), rest[idx+1:]...)
	} else {
		p.Text = string(rest)
	}
	return p, true
}

func binaryBE64(b []byte) uint64 {
	var v uint64
	for i := 0; i < 8 && i < len(b); i++ {
		v = v<<8 | uint64(b[i])
	}
	return v
}

func indexByte(s []byte, c byte) int {
	for i := range s {
		if s[i] == c {
			return i
		}
	}
	return -1
}
