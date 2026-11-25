package export

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"

	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/mp4"
	"github.com/zld/media-check/internal/stream"
)

func exportAudio(opts Options, track mp4.Track, samples map[uint32]map[int]stream.SampleReport) (AudioStat, *fault.Error) {
	indexes := indexesForTrack(opts.Manifest, track.TrackID)
	pcm := []byte{}
	peak := 0
	sampleCount := 0
	sumSquares := 0.0
	var lastOffset int64

	for _, idx := range indexes {
		sr := samples[track.TrackID][idx]
		payload, fe := payloadFromReport(opts.Data, sr, opts.InputID, opts.InputSHA)
		if fe != nil {
			return AudioStat{}, fe
		}
		if len(payload)%2 != 0 {
			return AudioStat{}, fault.E(fault.StageExport, fault.CodeSampleOutOfRange,
				"audio payload is not 16-bit aligned", fault.WithTrack(track.TrackID),
				fault.WithOffset(sr.AbsoluteOffset, int64(sr.Size)))
		}
		for off := 0; off+2 <= len(payload); off += 2 {
			v := int16(binary.LittleEndian.Uint16(payload[off : off+2]))
			a := int(v)
			if a < 0 {
				a = -a
			}
			if a > peak {
				peak = a
			}
			sumSquares += float64(v) * float64(v)
		}
		sampleCount += len(payload) / 2
		pcm = append(pcm, payload...)
		lastOffset = sr.AbsoluteOffset
	}

	artifact := ""
	if opts.OutDir != "" && len(pcm) > 0 {
		artifact = "audio/track-2.pcm"
		path := filepath.Join(opts.OutDir, artifact)
		if err := os.WriteFile(path, pcm, 0o644); err != nil {
			return AudioStat{}, fault.Wrap(fault.StageExport, fault.CodeReadFailed, err, "write audio artifact",
				fault.WithPath(artifact))
		}
	}
	sum := sha256.Sum256(pcm)
	rms := 0.0
	if sampleCount > 0 {
		rms = math.Sqrt(sumSquares / float64(sampleCount))
	}
	return AudioStat{
		TrackID: track.TrackID, Samples: sampleCount, Peak: peak,
		PeakDBFS: peakDBFS(peak), RMS: rms, SHA256: hex.EncodeToString(sum[:]), Artifact: artifact,
		Provenance: Provenance{InputID: opts.InputID, InputSHA256: opts.InputSHA,
			Stage: fault.StageExport, Artifact: artifact, ByteOffset: lastOffset},
	}, nil
}

func peakDBFS(peak int) float64 {
	if peak <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(float64(peak)/32767.0)
}
