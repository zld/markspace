package pipeline

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"time"

	"mediad/internal/fingerprint"
	"mediad/internal/ingest"
	"mediad/internal/mp4"
	"mediad/internal/silence"
	"mediad/internal/splitter"
	"mediad/internal/timeline"
)

// digestInputs hashes the ordered concatenation of file names and bytes so
// the WAL binding changes whenever any input changes.
func digestInputs(inputs []InputFile) (string, error) {
	h := sha256.New()
	for _, in := range inputs {
		nameLen := make([]byte, 4)
		binary.BigEndian.PutUint32(nameLen, uint32(len(in.Name)))
		h.Write(nameLen)
		h.Write([]byte(in.Name))
		sizeLen := make([]byte, 8)
		binary.BigEndian.PutUint64(sizeLen, uint64(len(in.Data)))
		h.Write(sizeLen)
		h.Write(in.Data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeJSON(path string, v any) error {
	// MarshalIndent then a trailing newline for stable, human-readable files.
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o600)
}

func walMillis() int64 { return time.Now().UnixMilli() }

// Reports converts groups to their serialisable reports.
func Reports(gs []*ingest.Group) []ingest.Report {
	out := make([]ingest.Report, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Summary())
	}
	return out
}

// flattenFrags returns the fragments of all groups in group order.
func flattenFrags(groups []*ingest.Group, fallback []*mp4.Fragment) []*mp4.Fragment {
	if len(groups) == 0 {
		return fallback
	}
	var out []*mp4.Fragment
	for _, g := range groups {
		out = append(out, g.Fragments...)
	}
	return out
}

func flattenSegments(m map[uint32][]splitter.Segment) []splitter.Segment {
	trackIDs := make([]uint32, 0, len(m))
	for id := range m {
		trackIDs = append(trackIDs, id)
	}
	sort.Slice(trackIDs, func(i, j int) bool { return trackIDs[i] < trackIDs[j] })
	var out []splitter.Segment
	for _, id := range trackIDs {
		out = append(out, m[id]...)
	}
	return out
}

func flattenGaps(groups []*ingest.Group) []ingest.Gap {
	var out []ingest.Gap
	for _, g := range groups {
		out = append(out, g.Gaps...)
	}
	return out
}

// assertMonotonic verifies that every valid mapped sample is non-decreasing
// on the common axis per track. Samples flagged Invalid are skipped because
// they represent intervals deliberately left unmapped rather than fabricated.
func assertMonotonic(mapped map[uint32][]splitter.MappedSample) error {
	trackIDs := make([]uint32, 0, len(mapped))
	for id := range mapped {
		trackIDs = append(trackIDs, id)
	}
	sort.Slice(trackIDs, func(i, j int) bool { return trackIDs[i] < trackIDs[j] })
	for _, id := range trackIDs {
		var last int64 = -1
		for _, s := range mapped[id] {
			if s.Invalid {
				continue
			}
			if s.StartUS < last {
				return timeline.New(timeline.ErrTimeline, s.Fragment, s.Offset,
					"track %d common-axis time went backwards: %d < %d", id, s.StartUS, last)
			}
			last = s.StartUS
		}
	}
	return nil
}

// buildPCMFrames treats fixture sample payloads as little-endian 16-bit PCM.
// Real deployments would plug a codec decoder here; the interface stays the
// same: decoded [-1,1] amplitudes per mapped sample.
func buildPCMFrames(trackID uint32, samples []splitter.MappedSample,
	frags []*mp4.Fragment, dataByFile map[string][]byte) []silence.PCMFrame {
	payloadByKey := map[string][]byte{}
	for _, f := range frags {
		data := dataByFile[f.File]
		for _, tf := range f.Tracks {
			if tf.TrackID != trackID {
				continue
			}
			for _, s := range tf.Samples {
				key := sampleKey(f.File, s.DataOffset)
				if s.DataOffset >= 0 && s.DataOffset+s.Size <= int64(len(data)) {
					payloadByKey[key] = data[s.DataOffset : s.DataOffset+s.Size]
				}
			}
		}
	}
	frames := make([]silence.PCMFrame, 0, len(samples))
	for _, s := range samples {
		// Mapped samples carry offset; look the raw bytes up via fragment data.
		var amps []float64
		// The mapped sample offset equals the underlying mp4 sample offset.
		for _, f := range frags {
			if p, ok := payloadByKey[sampleKey(f.File, s.Offset)]; ok {
				amps = pcm16ToFloat(p)
				break
			}
		}
		frames = append(frames, silence.PCMFrame{
			TrackID: trackID, StartUS: s.StartUS, EndUS: s.EndUS,
			Samples: amps, Invalid: s.Invalid,
		})
	}
	return frames
}

func sampleKey(file string, off int64) string {
	return file + ":" + strconv.FormatInt(off, 10)
}

// pcm16ToFloat decodes little-endian signed 16-bit PCM to [-1,1].
func pcm16ToFloat(b []byte) []float64 {
	n := len(b) / 2
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		v := int16(binary.LittleEndian.Uint16(b[i*2 : i*2+2]))
		out[i] = float64(v) / 32768.0
	}
	return out
}

// manifest is the deterministic final output structure.
type manifest struct {
	Segments         []splitter.Segment       `json:"segments"`
	SilenceIntervals []silence.Interval       `json:"silence_intervals"`
	Duplicates       []fingerprint.Fingerprint `json:"duplicates"`
	SequenceGaps     []ingest.Gap             `json:"sequence_gaps"`
}

func buildManifest(segments map[uint32][]splitter.Segment,
	silences []silence.Interval, fps []fingerprint.Fingerprint,
	groups []*ingest.Group) manifest {
	return manifest{
		Segments:         flattenSegments(segments),
		SilenceIntervals: silences,
		Duplicates:       fps,
		SequenceGaps:     flattenGaps(groups),
	}
}
