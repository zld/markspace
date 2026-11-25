package kernel

import (
	"encoding/binary"
	"fmt"
)

// BuildTSPacket is a deterministic fixture encoder for 188-byte TS packets.
// It is intentionally small and only implements the fields used by the kernel.
func BuildTSPacket(pid uint16, cc uint8, payload []byte, pusi bool, pcrBase *uint64) ([]byte, error) {
	if pid > 0x1fff {
		return nil, fmt.Errorf("PID 0x%x out of range", pid)
	}
	maxPayload := 184
	if pcrBase != nil {
		maxPayload = 176
	}
	if len(payload) > maxPayload {
		return nil, fmt.Errorf("payload %d exceeds %d", len(payload), maxPayload)
	}

	out := make([]byte, TSPacketSize)
	out[0] = TSSyncByte
	out[1] = byte((pid >> 8) & 0x1f)
	if pusi {
		out[1] |= 0x40
	}
	out[2] = byte(pid)
	out[3] = (cc & 0x0f) | 0x10 // payload only
	payloadOff := 4

	if pcrBase != nil {
		raw := *pcrBase & (PCRBasePeriod - 1)
		out[3] = (cc & 0x0f) | 0x30 // adaptation + payload
		out[4] = 7                  // flags byte + six PCR bytes
		out[5] = 0x10               // PCR flag
		pcr := (raw << 15) | (0x3f << 9)
		binary.BigEndian.PutUint16(out[6:8], uint16(pcr>>32))
		binary.BigEndian.PutUint32(out[8:12], uint32(pcr))
		payloadOff = 12
	}

	copy(out[payloadOff:], payload)
	for i := payloadOff + len(payload); i < TSPacketSize; i++ {
		out[i] = 0xff
	}
	return out, nil
}
