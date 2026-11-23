package engine

import (
	"bytes"
	"fmt"

	"mediad/adapter"
	"mediad/timeline"
)

// maxRTPSequence bounds the synthesized RTP stream so the 16-bit sequence
// space is never exhausted by the offline fixtures.
const maxRTPSequence = 65535

// generateTracks builds every input in-process: an MP4 video track plus a
// subtitle/chapter metadata stream. Fault fixtures mutate the bytes before
// parsing so truncation/corruption is exercised through real adapters.
func generateTracks(cfg Config, framesN, frameBytes int) ([]timeline.Track, error) {
	sizes := make([]uint32, framesN)
	keys := []uint32{1}
	for i := range sizes {
		sizes[i] = uint32(frameBytes)
		if i > 0 && i%25 == 0 {
			keys = append(keys, uint32(i+1))
		}
	}
	mp4Bytes, err := adapter.EncodeMP4(adapter.MP4Spec{
		Timescale: uint32(cfg.FPS), Delta: 1, Sizes: sizes, Keyframes: keys,
		Emsg: [][]byte{{0x65, 0x6d, 0x73, 0x67}},
	})
	if err != nil {
		return nil, err
	}
	switch {
	case cfg.Fixture == "truncated" && !cfg.Recover:
		// Pure truncation must propagate from the parser and fail the run.
		mp4Bytes = mp4Bytes[:len(mp4Bytes)*8/10]
	case cfg.Fixture == "truncated" && cfg.Recover:
		// Recovery scenario: the interrupted capture produced a corrupt keyframe
		// sample (damaged bytes). The parser marks it; the renderer isolates the
		// bad frame and resumes from the last complete checkpoint.
		mp4Bytes = corruptKeyframe(mp4Bytes)
	case cfg.Fixture == "corrupt":
		mp4Bytes = corruptKeyframe(mp4Bytes)
	}
	mp4Res, err := adapter.MP4Adapter{}.Parse(bytes.NewReader(mp4Bytes), adapter.Options{})
	if err != nil {
		return nil, fmt.Errorf("parse failure (fixture=%q): %w", cfg.Fixture, err)
	}

	// Metadata track: subtitles + chapters + one unknown preserved item.
	mw := adapter.NewMetadataWriter()
	for i := 0; i < framesN; i += cfg.FPS * 2 {
		mw.WriteRecord(2, int64(i*1000/cfg.FPS), byte(adapter.KindSubtitleForFixture()),
			0, "webvtt", []byte(fmt.Sprintf("SUB %d", i/cfg.FPS)))
	}
	for i := 0; i < int(cfg.Duration.Seconds()); i += 4 {
		mw.WriteRecord(3, int64(i*1000), byte(adapter.KindChapterForFixture()), 0,
			"chapter", []byte(fmt.Sprintf("CH%d", i/4)))
	}
	mw.WriteRecord(4, 100, 211, 0, "x-id3-private", []byte{0x01, 0x02, 0x03})
	metaRes, err := adapter.MetadataAdapter{}.Parse(bytes.NewReader(mw.Bytes()),
		adapter.Options{Timescale: 1000})
	if err != nil {
		return nil, err
	}

	// RTP capture track: a second device feeding video sampling points on a
	// 90kHz clock, exercising 32-bit timestamp handling inside the merge.
	rtpTrack, err := generateRTPTrack(cfg, framesN)
	if err != nil {
		return nil, err
	}
	out := append(mp4Res.Tracks, metaRes.Tracks...)
	out = append(out, rtpTrack)
	return out, nil
}

// generateRTPTrack synthesizes an RTP packet stream whose timestamps start
// near the 32-bit wrap boundary so the unified timeline proves unwrapping.
func generateRTPTrack(cfg Config, framesN int) (timeline.Track, error) {
	if framesN > maxRTPSequence {
		return timeline.Track{}, fmt.Errorf("rtp fixture: %d frames exceed 16-bit sequence space", framesN)
	}
	rw := adapter.NewRTPWriter()
	const clockRate = 90000
	ticksPerFrame := uint32(clockRate / cfg.FPS)
	// Place the whole stream just before the 32-bit wrap boundary so raw
	// timestamps wrap naturally; normalization unwraps them back into range.
	ts := uint32(0) - uint32(framesN)*ticksPerFrame
	for i := 0; i < framesN; i++ {
		marker := i%25 == 0
		pkt := adapter.NewRTPPacket(7, uint16(i), ts, marker, 64)
		if err := rw.WritePacket(pkt); err != nil {
			return timeline.Track{}, err
		}
		ts += ticksPerFrame
	}
	res, err := adapter.RTPAdapter{}.Parse(bytes.NewReader(rw.Bytes()),
		adapter.Options{TrackID: 5, Timescale: clockRate})
	if err != nil {
		return timeline.Track{}, err
	}
	return res.Tracks[0], nil
}

// corruptKeyframe flips the magic bytes of the second keyframe sample so the
// MP4 adapter marks that sample corrupt while the container stays parseable.
func corruptKeyframe(mp4Bytes []byte) []byte {
	out := append([]byte(nil), mp4Bytes...)
	first := bytes.Index(out, []byte("MEDI"))
	if first < 0 {
		return out
	}
	second := bytes.Index(out[first+4:], []byte("MEDI"))
	if second < 0 {
		return out
	}
	pos := first + 4 + second
	for i := 0; i < 4; i++ {
		out[pos+i] ^= 0xFF
	}
	return out
}
