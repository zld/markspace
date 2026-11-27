// Package fixture generates reproducible local sample data: HLS and DASH
// manifests plus fMP4 segments whose mdat payloads are synthesized RTP
// packets. Corrupt and boundary cases (truncated segment, backward
// timestamps, missing duration) are generated deterministically.
package fixture

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediacheck/internal/timeline"
)

// Case selects the fixture variant to generate.
type Case string

const (
	CaseNormal     Case = "normal"
	CaseTruncated  Case = "truncated"
	CaseBackward   Case = "backward"
	CaseNoDuration Case = "noduration"
)

const timescale = 90000

// Generate writes the fixture set for c into out/fixtures.
func Generate(out string, c Case) error {
	hlsDir := filepath.Join(out, "fixtures", "hls")
	dashDir := filepath.Join(out, "fixtures", "dash")
	for _, d := range []string{hlsDir, dashDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	truncateAt := -1
	if c == CaseTruncated {
		truncateAt = 1 // dash chunk1.m4s is cut mid-file
	}
	if err := writeSegments(hlsDir, "seg", 3, -1); err != nil {
		return err
	}
	if err := writeSegments(dashDir, "chunk", 3, truncateAt); err != nil {
		return err
	}
	init := box("ftyp", []byte("isom\x00\x00\x00\x00isom"))
	if err := os.WriteFile(filepath.Join(dashDir, "init.mp4"), init, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(hlsDir, "playlist.m3u8"), []byte(hlsPlaylist(c)), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dashDir, "manifest.mpd"), []byte(dashMPD(c)), 0o644)
}

func hlsPlaylist(c Case) string {
	s := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:0\n"
	s += "#EXTINF:1.000000,\nseg0.m4s\n"
	if c != CaseNoDuration {
		s += "#EXTINF:1.000000,\n"
	}
	s += "seg1.m4s\n#EXT-X-DISCONTINUITY\n#EXTINF:1.000000,\nseg2.m4s\n#EXT-X-ENDLIST\n"
	return s
}

func dashMPD(c Case) string {
	tl := `<S t="0" d="90000" r="2"/>`
	if c == CaseBackward {
		// second S starts before the first one ends: backward time
		tl = `<S t="0" d="90000"/><S t="45000" d="90000" r="1"/>`
	}
	return `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <Representation id="v1" bandwidth="800000">
        <SegmentTemplate timescale="90000" initialization="init.mp4" media="chunk$Number$.m4s" startNumber="0">
          <SegmentTimeline>` + tl + `</SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`
}

// writeSegments writes count fMP4 segments; truncateAt >= 0 cuts that
// segment to half its size to simulate an interrupted transfer.
func writeSegments(dir, prefix string, count, truncateAt int) error {
	ext := &timeline.RTPExtender{}
	for i := 0; i < count; i++ {
		payload, tfdt := rtpPayload(ext, i)
		data := fmp4Segment(tfdt, payload)
		if i == truncateAt {
			data = data[:len(data)/2]
		}
		name := fmt.Sprintf("%s%d.m4s", prefix, i)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// rtpPayload builds two RTP packets for segment i and returns their
// marshaled bytes plus the 64-bit extended decode time of the first packet.
func rtpPayload(ext *timeline.RTPExtender, seg int) ([]byte, uint64) {
	var payload []byte
	var tfdt uint64
	for i := 0; i < 2; i++ {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(seg*2 + i),
				Timestamp:      uint32(seg*timescale + i*timescale/2),
				SSRC:           0x01020304,
			},
			Payload: []byte{byte(seg), byte(i), 0xab, 0xcd},
		}
		if ts := ext.Extend(pkt); i == 0 {
			tfdt = ts
		}
		b, err := pkt.Marshal()
		if err != nil {
			panic(err)
		}
		payload = append(payload, b...)
	}
	return payload, tfdt
}

// box builds one ISO BMFF box with a 32-bit size header.
func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(len(b)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

// fmp4Segment builds a minimal media segment: styp, moof (mfhd, traf with
// tfhd/tfdt/trun) and mdat carrying the RTP payload.
func fmp4Segment(tfdt uint64, mdat []byte) []byte {
	mfhd := box("mfhd", []byte{0, 0, 0, 0, 0, 0, 0, 1})
	tfhd := box("tfhd", []byte{0, 0, 0, 0, 0, 0, 0, 1})
	tfdtPayload := make([]byte, 8) // version 0, 32-bit decode time
	binary.BigEndian.PutUint32(tfdtPayload[4:], uint32(tfdt))
	trun := box("trun", make([]byte, 8)) // version/flags + sample_count 0
	traf := box("traf", concat(tfhd, box("tfdt", tfdtPayload), trun))
	moof := box("moof", concat(mfhd, traf))
	styp := box("styp", []byte("msdh\x00\x00\x00\x00msix"))
	return concat(styp, moof, box("mdat", mdat))
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
