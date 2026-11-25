package export

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/mp4"
	"github.com/zld/media-check/internal/stream"
	"github.com/zld/media-check/internal/timeline"
)

type Options struct {
	Data     []byte
	InputID  string
	InputSHA string
	OutDir   string
	Manifest *mp4.Manifest
	Scan     *stream.Result
	Timeline *timeline.Report
}

// Run extracts fully readable elementary payloads, writes offscreen PPM/PCM
// artifacts, and computes deterministic hashes and statistics.
func Run(opts Options) (*Result, []*fault.Error) {
	var issues []*fault.Error
	res := &Result{Frames: []FrameStat{}, Segments: []SegmentStat{}}
	if opts.Manifest == nil || opts.Scan == nil {
		return res, []*fault.Error{fault.E(fault.StageExport, fault.CodeInvalidArgument, "missing manifest or scan result")}
	}

	sampleMap := map[uint32]map[int]stream.SampleReport{}
	for _, s := range opts.Scan.Samples {
		if sampleMap[s.TrackID] == nil {
			sampleMap[s.TrackID] = map[int]stream.SampleReport{}
		}
		sampleMap[s.TrackID][s.Index] = s
	}

	videoTrack := trackByKind(opts.Manifest, "video")
	audioTrack := trackByKind(opts.Manifest, "audio")
	if videoTrack == nil {
		issues = append(issues, fault.E(fault.StageExport, fault.CodeInvalidArgument, "missing video track"))
		return res, issues
	}

	if opts.OutDir != "" {
		if err := os.MkdirAll(filepath.Join(opts.OutDir, "frames"), 0o755); err != nil {
			issues = append(issues, fault.Wrap(fault.StageExport, fault.CodeReadFailed, err, "create frames directory"))
			return res, issues
		}
		if audioTrack != nil {
			_ = os.MkdirAll(filepath.Join(opts.OutDir, "audio"), 0o755)
		}
	}

	min, max := 255, 0
	pixelSum := 0
	pixelCount := 0
	var frameHashes []string
	videoIndexes := indexesForTrack(opts.Manifest, videoTrack.TrackID)
	for _, idx := range videoIndexes {
		sr := sampleMap[videoTrack.TrackID][idx]
		payload, fe := payloadFromReport(opts.Data, sr, opts.InputID, opts.InputSHA)
		if fe != nil {
			issues = append(issues, fe)
			continue
		}
		want := videoTrack.Width * videoTrack.Height * 3
		if len(payload) != want {
			issues = append(issues, fault.E(fault.StageExport, fault.CodeSampleOutOfRange,
				fmt.Sprintf("frame %d payload size %d != %d", idx, len(payload), want),
				fault.WithTrack(videoTrack.TrackID), fault.WithOffset(sr.AbsoluteOffset, int64(sr.Size))))
			continue
		}
		fmin, fmax, fsum := pixelStats(payload)
		hash := sha256.Sum256(payload)
		hashHex := hex.EncodeToString(hash[:])
		artifact := fmt.Sprintf("frames/frame-%02d.ppm", idx)
		if opts.OutDir != "" {
			if err := writePPM(filepath.Join(opts.OutDir, artifact), videoTrack.Width, videoTrack.Height, payload); err != nil {
				issues = append(issues, fault.Wrap(fault.StageExport, fault.CodeReadFailed, err, "write frame artifact",
					fault.WithPath(artifact)))
			}
		}
		res.Frames = append(res.Frames, FrameStat{
			Index: idx, TrackID: videoTrack.TrackID, PTSMS: sr.PTSMS, SHA256: hashHex,
			Width: videoTrack.Width, Height: videoTrack.Height, PixelMin: fmin, PixelMax: fmax,
			PixelMean: float64(fsum) / float64(len(payload)), Artifact: artifact,
			Provenance: Provenance{InputID: opts.InputID, InputSHA256: opts.InputSHA,
				Stage: fault.StageExport, Artifact: artifact, ByteOffset: sr.AbsoluteOffset},
		})
		frameHashes = append(frameHashes, hashHex)
		if fmin < min {
			min = fmin
		}
		if fmax > max {
			max = fmax
		}
		for _, v := range payload {
			pixelSum += int(v)
		}
		pixelCount += len(payload)
	}
	if pixelCount > 0 {
		res.Pixels = PixelSummary{Min: min, Max: max, Mean: float64(pixelSum) / float64(pixelCount)}
	}
	res.FrameSHA256 = aggregate(frameHashes)

	if audioTrack != nil {
		audio, fe := exportAudio(opts, *audioTrack, sampleMap)
		if fe != nil {
			issues = append(issues, fe)
		} else {
			res.Audio = audio
		}
	}

	res.Segments = exportSegments(opts.Manifest.Segments, res.Frames, opts)
	segHashes := make([]string, 0, len(res.Segments))
	for _, s := range res.Segments {
		segHashes = append(segHashes, s.SHA256)
	}
	res.SegmentSHA256 = aggregate(segHashes)
	if opts.Timeline != nil {
		res.AlignmentErrorMS = opts.Timeline.Alignment.MaxErrorMS
	}
	res.AlignmentProvenance = Provenance{InputID: opts.InputID, InputSHA256: opts.InputSHA, Stage: fault.StageTimeline}
	return res, issues
}

func trackByKind(m *mp4.Manifest, kind string) *mp4.Track {
	for i := range m.Tracks {
		if m.Tracks[i].Kind == kind {
			return &m.Tracks[i]
		}
	}
	return nil
}
