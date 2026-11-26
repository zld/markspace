package fixture

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"

	"github.com/pion/rtp"
)

// writeWebVTT emits one-second cues spanning the clip so the scan has multiple
// timed events on the 1/1000 WebVTT clock.
func writeWebVTT(path string, duration float64) error {
	var b strings.Builder
	b.WriteString("WEBVTT - generated sample\n\n")
	n := int(duration)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%02d:%02d:%02d.000 --> %02d:%02d:%02d.500\ncue %d\n\n",
			0, 0, i, 0, 0, i+1, i+1)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// writeChapters emits two chapter marks in the CHAPTERnn/NAME convention.
func writeChapters(path string) error {
	content := strings.Join([]string{
		"CHAPTER01=00:00:00.000",
		"CHAPTER01NAME=Opening",
		"CHAPTER02=00:00:02.000",
		"CHAPTER02NAME=Main body",
		"",
	}, "\n")
	return os.WriteFile(path, []byte(content), 0o644)
}

// writeID3 emits one ID3v2.4 tag with two frames whose payloads carry a leading
// big-endian millisecond presentation timestamp (the layout scan.ScanID3 reads).
func writeID3(path string) error {
	frame := func(id string, tickMs uint32, body []byte) []byte {
		payload := append(u32be(tickMs), body...)
		out := []byte(id)
		out = append(out, u32be(uint32(len(payload)))...)
		out = append(out, 0, 0) // frame flags
		return append(out, payload...)
	}
	frames := append(frame("TXXX", 1500, []byte("marker-a")),
		frame("PRIV", 4000, []byte("owner\x00data"))...)
	tag := []byte("ID3\x04\x00\x00")
	tag = append(tag, syncSafe(len(frames))...)
	tag = append(tag, frames...)
	return os.WriteFile(path, tag, 0o644)
}

// syncSafe encodes a 28-bit syncsafe integer.
func syncSafe(n int) []byte {
	return []byte{byte(n >> 21), byte(n >> 14), byte(n >> 7), byte(n)}
}

func u32be(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// writeRTPSEI writes concatenated H.264 RTP packets on the 90 kHz clock, each
// carrying one SEI NAL unit, so the scan extractor has parseable timed SEI.
func writeRTPSEI(path string, duration float64) error {
	var out []byte
	seq := uint16(0)
	n := int(duration)
	for i := 0; i < n; i++ {
		// H.264 SEI NAL: header type 6, one user-data-unregistered message
		// (type 5), then RBSP stop bit 0x80.
		nal := []byte{6, 5, 2, byte('A' + i), 0x80}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96, SequenceNumber: seq,
				Timestamp: uint32(i * 90000), SSRC: 0x42, Marker: true,
			},
			Payload: nal,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return err
		}
		// Length-prefix each packet so a reader can frame the stream.
		out = append(out, u32be(uint32(len(raw)))...)
		out = append(out, raw...)
		seq++
	}
	return os.WriteFile(path, out, 0o644)
}

// ReadRTPStream frames the length-prefixed RTP packets written by writeRTPSEI.
func ReadRTPStream(data []byte) ([][]byte, error) {
	var packets [][]byte
	i := 0
	for i+4 <= len(data) {
		n := int(binary.BigEndian.Uint32(data[i:]))
		i += 4
		if n <= 0 || i+n > len(data) {
			return nil, fmt.Errorf("fixture: truncated RTP framing at %d", i)
		}
		packets = append(packets, data[i:i+n])
		i += n
	}
	return packets, nil
}
