package mp4

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"mediad/internal/clock"
	"mediad/internal/fault"
)

// TrackResult is the verified content of one track inside a parsed segment.
type TrackResult struct {
	TrackID       uint32
	SampleRate    uint32
	BitsPerSample uint16
	Frames        uint64
	PCM           []byte
}

// ParseResult is everything recovered by re-parsing a segment file.
type ParseResult struct {
	Tracks     []TrackResult
	Range      clock.FrameRange
	Keyframes  []uint32 // sample numbers flagged in stss
}

// ReadSegment re-parses a complete MP4 segment with go-mp4, follows the stbl
// index tables and extracts the exact mdat payload for every track.
func ReadSegment(r io.ReadSeeker, want clock.FrameRange) (*ParseResult, error) {
	info, err := gomp4.Probe(r)
	if err != nil {
		return nil, fault.Wrap(fault.CatWrite, "mp4.probe", -1, -1, true, err)
	}
	if len(info.Tracks) == 0 {
		return nil, fault.New(fault.CatWrite, "mp4.probe", true,
			"re-parsed segment contains no tracks")
	}
	out := &ParseResult{Range: want}
	for ti, tr := range info.Tracks {
		if len(tr.Chunks) == 0 {
			return nil, fault.New(fault.CatWrite, "mp4.probe", true,
				"track has no indexed chunks")
		}
		chunk := tr.Chunks[0]
		entry := extractAudioEntry(r, tr.TrackID)
		// mp4a.SampleSize is expressed in bits; the byte width is /8.
		// PCM uses constant sizes, so stsz keeps its per-sample array empty
		// and Probe leaves each Sample.Size at zero.
		bitsPerSample := entry.sampleSize
		if bitsPerSample == 0 || bitsPerSample%8 != 0 {
			return nil, fault.New(fault.CatWrite, "mp4.probe", true,
				"invalid PCM sample width in stsd")
		}
		sampleBytes := int(bitsPerSample) / 8
		sampleCount := len(tr.Samples)
		if sampleCount == 0 {
			return nil, fault.New(fault.CatWrite, "mp4.probe", true,
				"track stts table declares no samples")
		}
		buf := make([]byte, sampleCount*sampleBytes)
		if _, err := r.Seek(int64(chunk.DataOffset), io.SeekStart); err != nil {
			return nil, wrapWrite(err)
		}
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, fault.Wrap(fault.CatWrite, "mp4.read", -1, ti, true, err)
		}
		out.Tracks = append(out.Tracks, TrackResult{
			TrackID:       tr.TrackID,
			SampleRate:    tr.Timescale,
			BitsPerSample: bitsPerSample,
			Frames:        uint64(sampleCount),
			PCM:           buf,
		})
	}
	keys, err := extractKeyframes(r)
	if err != nil {
		return nil, err
	}
	out.Keyframes = keys
	return out, nil
}

type audioEntry struct {
	sampleSize uint16
}

func extractAudioEntry(r io.ReadSeeker, trackID uint32) audioEntry {
	boxes, err := gomp4.ExtractBoxesWithPayload(r, nil, []gomp4.BoxPath{
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(),
			gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStsd(), gomp4.BoxTypeMp4a()},
	})
	if err != nil || len(boxes) == 0 {
		return audioEntry{}
	}
	_ = trackID
	ase := boxes[0].Payload.(*gomp4.AudioSampleEntry)
	return audioEntry{sampleSize: ase.SampleSize}
}

func extractKeyframes(r io.ReadSeeker) ([]uint32, error) {
	boxes, err := gomp4.ExtractBoxesWithPayload(r, nil, []gomp4.BoxPath{
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(),
			gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStss()},
	})
	if err != nil {
		return nil, fault.Wrap(fault.CatWrite, "mp4.stss", -1, -1, true, err)
	}
	var keys []uint32
	for _, b := range boxes {
		stss := b.Payload.(*gomp4.Stss)
		keys = append(keys, stss.SampleNumber...)
	}
	return keys, nil
}

// Verify compares a re-parsed segment against the original spec. Every sample
// byte must match and the covered frame range must be preserved.
func Verify(seg Segment, got *ParseResult) error {
	if len(got.Tracks) != len(seg.Tracks) {
		return fault.New(fault.CatHash, "mp4.verify", true,
			"re-parsed track count differs from the segment spec")
	}
	for i, tr := range got.Tracks {
		if tr.TrackID != seg.Tracks[i].TrackID {
			return fault.New(fault.CatHash, "mp4.verify", true,
				"re-parsed track id mismatch")
		}
		if tr.SampleRate != seg.Tracks[i].SampleRate ||
			tr.BitsPerSample != seg.Tracks[i].BitsPerSample {
			return fault.New(fault.CatHash, "mp4.verify", true,
				"re-parsed audio format differs from the segment spec")
		}
		if !bytes.Equal(tr.PCM, seg.PCM[i]) {
			return fault.New(fault.CatHash, "mp4.verify", true,
				"re-parsed PCM payload differs from the muxed input")
		}
		if tr.Frames != seg.Frames.Len() {
			return fault.New(fault.CatHash, "mp4.verify", true,
				"re-parsed frame count does not cover the segment range")
		}
	}
	if got.Range.Start != seg.Frames.Start || got.Range.End != seg.Frames.End {
		return fault.New(fault.CatHash, "mp4.verify", true,
			"re-parsed frame range differs from the segment spec")
	}
	if len(got.Keyframes) == 0 {
		return fault.New(fault.CatHash, "mp4.verify", true,
			"segment lost its keyframe index")
	}
	return nil
}

// Hash returns the SHA-256 over the concatenated per-track PCM payload in
// track order. The same segment content always produces the same digest, so
// checkpoints can re-validate files after a restart.
func Hash(seg Segment) string {
	h := sha256.New()
	for _, pcm := range seg.PCM {
		h.Write(pcm)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// HashResult computes the same digest over re-parsed content.
func HashResult(got *ParseResult) string {
	h := sha256.New()
	for _, tr := range got.Tracks {
		h.Write(tr.PCM)
	}
	return hex.EncodeToString(h.Sum(nil))
}
