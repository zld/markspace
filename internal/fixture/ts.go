package fixture

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// HLS layout of the generated playlist.
const (
	HLSSegments   = 4   // number of TS segments
	HLSPESPerSeg  = 60  // PES packets per segment
	HLSPESSpacing = 3000 // PTS spacing between PES packets (1/30 s)
	// HLSDiscontinuityAt is the segment index before which the
	// EXT-X-DISCONTINUITY tag and the PTS jump are inserted.
	HLSDiscontinuityAt = 2
)

// writeHLS writes playlist.m3u8 and its TS segments. The PTS of the
// segment after the discontinuity jumps backwards, which the timeline
// must absorb while staying monotonic.
func writeHLS(dir string) error {
	var pl bytes.Buffer
	pl.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
	pts := int64(126000) // 1.4 s initial offset
	for i := 0; i < HLSSegments; i++ {
		if i == HLSDiscontinuityAt {
			pl.WriteString("#EXT-X-DISCONTINUITY\n")
			pts = 9000 // cross-discontinuity backward jump
		}
		pl.WriteString("#EXTINF:2.000,\n")
		name := fmt.Sprintf("seg%d.ts", i)
		pl.WriteString(name + "\n")
		if err := os.WriteFile(filepath.Join(dir, name), EncodeTS(pts), 0o644); err != nil {
			return err
		}
		pts += HLSPESPerSeg * HLSPESSpacing
	}
	pl.WriteString("#EXT-X-ENDLIST\n")
	return os.WriteFile(filepath.Join(dir, "playlist.m3u8"), pl.Bytes(), 0o644)
}

// EncodeTS builds one TS segment: HLSPESPerSeg PES packets, each in its
// own TS packet, PTS spaced HLSPESSpacing ticks apart.
func EncodeTS(startPTS int64) []byte {
	var buf bytes.Buffer
	for i := 0; i < HLSPESPerSeg; i++ {
		pts := startPTS + int64(i)*HLSPESSpacing
		pkt := make([]byte, 188)
		pkt[0] = 0x47
		pkt[1] = 0x41 // PUSI set, PID 0x100
		pkt[2] = 0x00
		pkt[3] = 0x10 // payload only
		pes := []byte{0x00, 0x00, 0x01, 0xe0, 0x00, 0x00, 0x80, 0x80, 0x05}
		pes = append(pes, encodePTS(pts)...)
		copy(pkt[4:], pes)
		buf.Write(pkt)
	}
	return buf.Bytes()
}

// encodePTS encodes a 33-bit PTS into its 5-byte form.
func encodePTS(pts int64) []byte {
	v := uint64(pts) & 0x1ffffffff
	return []byte{
		0x20 | byte((v>>30)&0x07)<<1 | 1,
		byte(v >> 22),
		byte((v>>15)&0x7f)<<1 | 1,
		byte(v >> 7),
		byte(v&0x7f)<<1 | 1,
	}
}
