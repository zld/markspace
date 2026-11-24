package fixture

import "encoding/binary"

// buildWAV assembles a canonical 44-byte-header PCM WAV byte slice from the
// generated payload. RIFF and data sizes are set for the pristine stream.
func buildWAV(cfg AudioConfig, pcm []byte) []byte {
	ba := cfg.BlockAlign()
	byteRate := cfg.SampleRate * ba
	buf := make([]byte, 44+len(pcm))
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+len(pcm)))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:24], uint16(cfg.Channels))
	binary.LittleEndian.PutUint32(buf[24:28], uint32(cfg.SampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(buf[32:34], uint16(ba))
	binary.LittleEndian.PutUint16(buf[34:36], uint16(cfg.BitsPerSample))
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(len(pcm)))
	copy(buf[44:], pcm)
	return buf
}
