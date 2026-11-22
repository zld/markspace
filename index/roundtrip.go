package index

import (
	"bytes"

	"mediacore"
)

// RoundtripReport captures one parse -> write -> parse cycle per track.
type RoundtripReport struct {
	Track          mediacore.TrackID
	SamplesMatch   bool
	TimesMatch     bool
	KeyframesMatch bool
	SampleCount    int
	KeyframeCount  int
}

// Roundtrip parses an MP4, rewrites it through the deterministic writer and
// parses the result again, asserting sample counts, presentation timestamps
// and keyframe positions are preserved exactly. Payload bytes are read from
// the original container via the recorded chunk offsets.
func Roundtrip(raw []byte, trackFor func(uint32) mediacore.TrackID) ([]RoundtripReport, []byte, error) {
	first, err := ParseMP4(trackFor, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	writeTracks, payloads, err := toWriteTracks(first, raw)
	if err != nil {
		return nil, nil, err
	}
	out, err := WriteMP4(writeTracks)
	if err != nil {
		return nil, nil, err
	}
	second, err := ParseMP4(trackFor, bytes.NewReader(out))
	if err != nil {
		return nil, nil, err
	}
	reports := make([]RoundtripReport, 0, len(first.Tracks))
	for i := range first.Tracks {
		reports = append(reports, compareTrack(first.Tracks[i], second.Tracks[i], payloads[i]))
	}
	return reports, out, nil
}

// toWriteTracks converts parsed tracks into writer inputs, slicing original
// payload bytes with the validated chunk offsets and sizes.
func toWriteTracks(c *Container, raw []byte) ([]WriteTrack, [][]byte, error) {
	wt := make([]WriteTrack, 0, len(c.Tracks))
	payloads := make([][]byte, 0, len(c.Tracks))
	for _, ti := range c.Tracks {
		deltas := make([]uint32, len(ti.Samples))
		sizes := make([]uint32, len(ti.Samples))
		keys := make([]bool, len(ti.Samples))
		var data []byte
		for i, s := range ti.Samples {
			// Preserve the raw timescale-unit delta exactly; converting via
			// nanoseconds would truncate non-divisible VFR intervals.
			deltas[i] = s.ScaleDelta
			sizes[i] = uint32(s.Size)
			keys[i] = s.Keyframe
			end := s.ChunkOffset + int64(s.Size)
			if s.ChunkOffset < 0 || end > int64(len(raw)) {
				return nil, nil, mediacore.NewError(mediacore.CatCorrupt, ti.Track,
					"mp4.offset", s.ChunkOffset, "sample offset outside container")
			}
			data = append(data, raw[s.ChunkOffset:end]...)
		}
		wt = append(wt, WriteTrack{
			TrackID: ti.TrackID, Timescale: ti.Timescale,
			IsVideo: ti.HasKeyframes, Deltas: deltas, Sizes: sizes,
			Keyframes: keys, SampleData: data,
		})
		payloads = append(payloads, data)
	}
	return wt, payloads, nil
}

func compareTrack(a, b TrackInfo, payload []byte) RoundtripReport {
	r := RoundtripReport{Track: a.Track, SampleCount: len(a.Samples),
		KeyframeCount: BuildKeyframeIndex(a).Count()}
	r.SamplesMatch = len(a.Samples) == len(b.Samples)
	r.TimesMatch = true
	r.KeyframesMatch = true
	if r.SamplesMatch {
		for i := range a.Samples {
			if a.Samples[i].Presentation != b.Samples[i].Presentation {
				r.TimesMatch = false
			}
			if a.Samples[i].Keyframe != b.Samples[i].Keyframe {
				r.KeyframesMatch = false
			}
			if a.Samples[i].Size != b.Samples[i].Size {
				r.SamplesMatch = false
			}
		}
	}
	_ = payload
	return r
}
