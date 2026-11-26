package fixtures

import (
	"encoding/binary"
	"fmt"

	"replaypipe/internal/mp4box"
)

// BuildTracks returns the healthy presentation: one H.264-like video track
// with a keyframe every two seconds and one AAC-like audio track. Sample
// payloads are deterministic synthetic bytes so hashes are reproducible.
func BuildTracks() []mp4box.Track {
	video := mp4box.Track{
		ID: 1, Handler: "vide", Timescale: Timescale,
		Width: Width, Height: Height, Volume: 0, Codec: "avc1",
		AvcC: avcConfig(),
	}
	for i := 0; i < Frames; i++ {
		sync := i%GOP == 0
		size := 300
		if sync {
			size = 900
		}
		video.Samples = append(video.Samples, mp4box.Sample{
			Data:     pattern(byte(0xA0+i%16), size, i),
			DTS:      uint64(i * FrameDur),
			Duration: FrameDur,
			Sync:     sync,
		})
	}
	audio := mp4box.Track{
		ID: 2, Handler: "soun", Timescale: 48000,
		SampleRate: 48000, Channel: 2, Codec: "mp4a", Volume: 0x0100,
	}
	const audioSamples = 48000 / 1024 * 10 // 10 seconds of 1024-frame AAC
	for i := 0; i < audioSamples; i++ {
		audio.Samples = append(audio.Samples, mp4box.Sample{
			Data:     pattern(byte(0x50+i%16), 180, i),
			DTS:      uint64(i * 1024),
			Duration: 1024,
		})
	}
	return []mp4box.Track{video, audio}
}

func pattern(seed byte, n, idx int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed ^ byte(i*7) ^ byte(idx*13)
	}
	if n >= 4 {
		// A NAL-like header keeps the payload plausible to the Annex B splitter.
		b[0], b[1], b[2], b[3] = 0x00, 0x00, 0x00, 0x01
		if n > 4 {
			b[4] = 0x65
		}
	}
	return b
}

// avcConfig builds a minimal AVCDecoderConfigurationRecord with one SPS and
// one PPS so the writer emits an avcC child box.
func avcConfig() []byte {
	sps := []byte{0x67, 0x42, 0xC0, 0x1E, 0xDA, 0x02, 0x80, 0xBF, 0xE5}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	out := []byte{
		0x01,        // configurationVersion
		sps[1],      // profile
		sps[2],      // compatibility
		sps[3],      // level
		0xFC | 0x03, // reserved + lengthSizeMinusOne = 3
		0xE0 | 0x01, // reserved + numSPS = 1
	}
	out = append(out, byte(len(sps)>>8), byte(len(sps)))
	out = append(out, sps...)
	out = append(out, 0x01) // numPPS
	out = append(out, byte(len(pps)>>8), byte(len(pps)))
	out = append(out, pps...)
	return out
}

// buildID3 returns an ID3v2.4 tag carrying three TXXX/PRIV timed events at the
// fixture cue times. Each PRIV frame holds the 33-bit MPEG-TS timestamp.
func buildID3() []byte {
	var frames []byte
	for i, e := range EventSeconds {
		frames = append(frames, id3Frame("TXXX", id3TextPayload(fmt.Sprintf("event-%d", i+1), e.Text))...)
		frames = append(frames, id3Frame("PRIV", id3PrivPayload(int64(e.Start)))...)
	}
	return id3Tag(frames)
}

func id3Frame(id string, payload []byte) []byte {
	out := make([]byte, 10)
	copy(out, id)
	// ID3v2.4 syncsafe frame size.
	sz := len(payload)
	out[4] = byte((sz >> 21) & 0x7f)
	out[5] = byte((sz >> 14) & 0x7f)
	out[6] = byte((sz >> 7) & 0x7f)
	out[7] = byte(sz & 0x7f)
	return append(out, payload...)
}

func id3TextPayload(desc, text string) []byte {
	out := []byte{0x03} // UTF-8
	out = append(out, []byte(desc)...)
	out = append(out, 0x00)
	out = append(out, []byte(text)...)
	return out
}

func id3PrivPayload(startSec int64) []byte {
	out := []byte{0x00}
	out = append(out, []byte(tsPrivOwner)...)
	out = append(out, 0x00)
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(startSec)*90000)
	out = append(out, buf...)
	return out
}

func id3Tag(frames []byte) []byte {
	size := len(frames)
	hdr := []byte{'I', 'D', '3', 0x04, 0x00, 0x00}
	hdr = append(hdr,
		byte((size>>21)&0x7f), byte((size>>14)&0x7f), byte((size>>7)&0x7f), byte(size&0x7f))
	return append(hdr, frames...)
}

const tsPrivOwner = "com.apple.streaming.transportStreamTimestamp"
