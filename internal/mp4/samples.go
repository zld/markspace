package mp4

import (
	"bytes"
	"io"
	"math"

	gomp4 "github.com/abema/go-mp4"

	"mediapipeline/internal/evidence"
	"mediapipeline/pkg/media"
)

type keyframeSet struct {
	numbers map[uint32]bool
	ordered []media.Keyframe
}

func extractKeyframeSets(r io.ReadSeeker) []keyframeSet {
	boxes, err := gomp4.ExtractBoxesWithPayload(r, nil, []gomp4.BoxPath{
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStss()},
	})
	if err != nil {
		return nil
	}
	out := make([]keyframeSet, 0, len(boxes))
	for _, bp := range boxes {
		stss, ok := bp.Payload.(*gomp4.Stss)
		if !ok {
			continue
		}
		set := keyframeSet{numbers: make(map[uint32]bool, len(stss.SampleNumber))}
		for _, n := range stss.SampleNumber {
			set.numbers[n] = true
			set.ordered = append(set.ordered, media.Keyframe{SampleNumber: n, Offset: keyframeOffset(r, n)})
		}
		out = append(out, set)
	}
	return out
}

func keyframeOffset(io.ReadSeeker, uint32) int64 { return -1 }

func attachSampleDescription(r io.ReadSeeker, locs []BoxLocation, trackID uint32, track *media.TrackInfo) {
	loc, ok := boxLocationForTrack(locs, "stsd", trackID)
	if !ok || loc.Size <= loc.HeaderSize || loc.HeaderSize < 0 {
		return
	}
	raw, err := readAt(r, loc.Offset, loc.Size)
	if err != nil || int(loc.HeaderSize) > len(raw) {
		return
	}
	track.SampleDescription = append([]byte(nil), raw[loc.HeaderSize:]...)
}

func keyframeSetForTrack(sets []keyframeSet, index int, trackID uint32) []media.Keyframe {
	if index < 0 || index >= len(sets) {
		// No stss means all samples are sync samples in ISO BMFF.
		return nil
	}
	return sets[index].ordered
}

func populateSamples(
	r io.ReadSeeker,
	pt *gomp4.Track,
	track *media.TrackInfo,
	locs []BoxLocation,
	fileSize int64,
	chunkBytes int,
	report *media.Report,
) error {
	if len(pt.Chunks) == 0 {
		report.Error(media.ClassBadSTCO, "track has no stco/co64 chunk offsets",
			media.WithTrack(track.TrackID), media.WithScope(media.ScopeTrack))
		return nil
	}

	var keyframes map[uint32]bool
	if len(track.Keyframes) > 0 {
		keyframes = make(map[uint32]bool, len(track.Keyframes))
		for _, k := range track.Keyframes {
			keyframes[k.SampleNumber] = true
		}
	}

	clock := media.Clock{Timebase: track.Timebase, StartTicks: track.StartTimeTicks}
	dts := int64(0)
	sampleIdx := 0
	var previousPTS int64
	havePTS := false
	backwardReported := false

	for chunkIndex, chunk := range pt.Chunks {
		if chunk == nil {
			continue
		}
		if chunk.DataOffset > math.MaxInt64 {
			addSTCOFault(report, locs, track.TrackID, chunkIndex, "uint64 offset overflows int64")
			continue
		}
		chunkOffset := int64(chunk.DataOffset)
		offsetInChunk := int64(0)
		perChunk := int(chunk.SamplesPerChunk)
		if perChunk <= 0 {
			perChunk = 1
		}

		for j := 0; j < perChunk; j++ {
			if sampleIdx >= len(pt.Samples) {
				break
			}
			ps := pt.Samples[sampleIdx]
			number := uint32(sampleIdx + 1)
			size := int64(ps.Size)
			absolute, overflow := safeOffset(chunkOffset, offsetInChunk)
			outOfBounds := overflow || size < 0 || absolute > fileSize || size > fileSize-absolute

			sample := media.Sample{
				Number:        number,
				TrackID:       track.TrackID,
				DTS:           dts,
				Duration:      ps.TimeDelta,
				Size:          ps.Size,
				Sync:          keyframes == nil || keyframes[number],
				ChunkNumber:   uint32(chunkIndex + 1),
				ChunkOffset:   chunkOffset,
				OffsetInChunk: offsetInChunk,
			}

			pts, err := clock.PTS(dts, ps.CompositionTimeOffset)
			if err != nil {
				report.Error(media.ClassOverflow, err.Error(), media.WithTrack(track.TrackID),
					media.WithBox("ctts", boxOffsetForTrack(locs, "ctts", track.TrackID)), media.WithScope(media.ScopeSegment))
			} else {
				sample.PTS = pts
				if havePTS && pts < previousPTS && !backwardReported {
					report.Error(media.ClassBackwardPTS,
						"presentation timestamp moves backwards",
						media.WithTrack(track.TrackID),
						media.WithBox("ctts", boxOffsetForTrack(locs, "ctts", track.TrackID)),
						media.WithScope(media.ScopeSegment))
					backwardReported = true
				}
				previousPTS = pts
				havePTS = true
			}

			if outOfBounds {
				addSTCOFault(report, locs, track.TrackID, chunkIndex,
					"sample data range is outside file")
			} else {
				data, err := readAt(r, absolute, size)
				if err != nil {
					return err
				}
				sample.Data = data
				sample.SHA256 = evidence.SHA256Bytes(data)
			}

			track.Samples = append(track.Samples, sample)
			if keyframes != nil && keyframes[number] {
				// Keep keyframe byte offsets aligned with expanded samples.
				for i := range track.Keyframes {
					if track.Keyframes[i].SampleNumber == number {
						track.Keyframes[i].Offset = absolute
					}
				}
			}

			offsetInChunk += size
			next, err := media.Advance(dts, ps.TimeDelta)
			if err != nil {
				report.Error(media.ClassOverflow, err.Error(), media.WithTrack(track.TrackID),
					media.WithBox("stts", boxOffsetForTrack(locs, "stts", track.TrackID)))
			} else {
				dts = next
			}
			sampleIdx++
		}
	}

	if sampleIdx != len(pt.Samples) {
		report.Error("stsc_sample_mismatch", "stsc/stsz sample expansion did not consume all samples",
			media.WithTrack(track.TrackID), media.WithScope(media.ScopeTrack))
	}
	track.Segments = BuildSegments(track.TrackID, track.Samples, chunkBytes)
	return nil
}

// BuildSegments groups samples into deterministic byte-bounded fragments.
func BuildSegments(trackID uint32, samples []media.Sample, chunkBytes int) []media.Segment {
	if chunkBytes <= 0 {
		chunkBytes = 1 << 20
	}
	var segments []media.Segment
	var current media.Segment
	var payload bytes.Buffer
	flush := func() {
		if current.FirstSample == 0 {
			return
		}
		current.Index = len(segments)
		current.TrackID = trackID
		current.Bytes = payload.Len()
		current.SHA256 = evidence.SHA256Bytes(payload.Bytes())
		segments = append(segments, current)
		current = media.Segment{}
		payload.Reset()
	}
	for _, s := range samples {
		if payload.Len() > 0 && payload.Len()+len(s.Data) > chunkBytes {
			flush()
		}
		if current.FirstSample == 0 {
			current.FirstSample = s.Number
		}
		current.LastSample = s.Number
		current.SampleSHA = append(current.SampleSHA, s.SHA256)
		payload.Write(s.Data)
	}
	flush()
	return segments
}

func hashTrack(t *media.TrackInfo) string {
	sum, err := evidence.HashJSON(t)
	if err != nil {
		return ""
	}
	return sum
}

func readAt(r io.ReadSeeker, offset, size int64) ([]byte, error) {
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

func safeOffset(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, true
	}
	return a + b, false
}

func addSTCOFault(report *media.Report, locs []BoxLocation, trackID uint32, chunkIndex int, msg string) {
	off := int64(-1)
	if loc, ok := boxLocationForTrack(locs, "stco", trackID); ok {
		off = loc.Offset + 16 + int64(chunkIndex)*4
	}
	report.Error(media.ClassBadSTCO, msg,
		media.WithTrack(trackID), media.WithBox("stco", off), media.WithScope(media.ScopeTrack))
}

func boxOffsetForTrack(locs []BoxLocation, typ string, trackID uint32) int64 {
	if loc, ok := boxLocationForTrack(locs, typ, trackID); ok {
		return loc.Offset
	}
	return -1
}

func boxLocationForTrack(locs []BoxLocation, typ string, trackID uint32) (BoxLocation, bool) {
	currentTrack := uint32(0)
	var fallback BoxLocation
	haveFallback := false
	for _, loc := range locs {
		if loc.Type == "tkhd" && loc.TrackID != 0 {
			currentTrack = loc.TrackID
		}
		if loc.Type == typ {
			fallback = loc
			haveFallback = true
			if currentTrack == trackID {
				return loc, true
			}
		}
	}
	// Single-track fixtures and parsers that did not expose tkhd payloads still
	// get a useful location.
	if trackID != 0 && len(trackIDsFromLocations(locs)) <= 1 && haveFallback {
		return fallback, true
	}
	return BoxLocation{}, false
}
