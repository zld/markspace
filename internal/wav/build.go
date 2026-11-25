package wav

import (
	"fmt"
	"io"
)

// BuildPCM constructs a canonical 44-byte-header RIFF/WAVE image in memory.
// It is used by fixtures and by the self-test to create deterministic PCM
// without external material. data is interleaved little-endian sample bytes.
func BuildPCM(channels, rate, bits int, data []byte) []byte {
	if channels <= 0 {
		channels = 1
	}
	if rate <= 0 {
		rate = 48000
	}
	if bits <= 0 {
		bits = 16
	}
	frame := channels * bits / 8
	payload := append([]byte{}, data...)
	if len(payload)%2 == 1 {
		payload = append(payload, 0) // RIFF chunk padding, not sample data
	}
	out := make([]byte, 44, 44+len(payload))
	copy(out[0:4], "RIFF")
	putU32(out[4:8], uint32(36+len(payload)))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	putU32(out[16:20], 16)
	putU16(out[20:22], 1) // PCM
	putU16(out[22:24], uint16(channels))
	putU32(out[24:28], uint32(rate))
	putU32(out[28:32], uint32(rate*frame))
	putU16(out[32:34], uint16(frame))
	putU16(out[34:36], uint16(bits))
	copy(out[36:40], "data")
	putU32(out[40:44], uint32(len(data)))
	return append(out, payload...)
}

func putU16(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}

func putU32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

// WritePCMHeader writes a canonical 44-byte RIFF/WAVE header declaring
// dataLen bytes of interleaved PCM. Callers stream the payload directly
// after it, so a long fragment never has to be materialised in memory.
func WritePCMHeader(w io.Writer, channels, rate, bits int, dataLen int64) (int64, error) {
	if channels <= 0 || rate <= 0 || bits <= 0 || dataLen < 0 {
		return 0, fmt.Errorf("wav: invalid header parameters channels=%d rate=%d bits=%d len=%d",
			channels, rate, bits, dataLen)
	}
	frame := channels * bits / 8
	var hdr [44]byte
	copy(hdr[0:4], "RIFF")
	putU32(hdr[4:8], uint32(36+dataLen))
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	putU32(hdr[16:20], 16)
	putU16(hdr[20:22], 1) // PCM
	putU16(hdr[22:24], uint16(channels))
	putU32(hdr[24:28], uint32(rate))
	putU32(hdr[28:32], uint32(rate*frame))
	putU16(hdr[32:34], uint16(frame))
	putU16(hdr[34:36], uint16(bits))
	copy(hdr[36:40], "data")
	putU32(hdr[40:44], uint32(dataLen))
	n, err := w.Write(hdr[:])
	return int64(n), err
}
