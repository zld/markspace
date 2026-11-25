package pipeline

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"mediacore/internal/clock"
	"mediacore/internal/index"
)

type transformArtifact struct {
	BatchID  string      `json:"batch_id"`
	Range    index.Range `json:"range"`
	Segments []Segment   `json:"segments"`
}

func (rn *Runner) runTransform() error {
	a := rn.data
	res, err := a.primary.Query(rn.Range)
	if err != nil {
		return err
	}
	a.result = res
	rn.Report.QuerySampleCount = len(res.Samples)
	if res.Keyframe != nil {
		rn.Report.KeyframeNumber = res.Keyframe.SampleNumber
	}

	n := rn.SegmentCount
	if n <= 0 {
		n = 3
	}
	width := (rn.Range.End - rn.Range.Start) / clock.EventTime(n)
	segments := make([]Segment, 0, n)
	hashes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		sr := index.Range{
			Start: rn.Range.Start + clock.EventTime(i)*width,
			End:   rn.Range.Start + clock.EventTime(i+1)*width,
		}
		if i == n-1 {
			sr.End = rn.Range.End
		}
		part, err := a.primary.Query(sr)
		if err != nil {
			return err
		}
		seg := Segment{Index: i, StartNS: int64(sr.Start), EndNS: int64(sr.End)}
		if part.Keyframe != nil {
			seg.KeyframeNumber = part.Keyframe.SampleNumber
			seg.KeyframeOffset = part.Keyframe.FileOffset
		}
		for _, s := range part.Samples {
			seg.SampleNumbers = append(seg.SampleNumbers, s.Number)
			seg.SampleOffsets = append(seg.SampleOffsets, s.Offset)
		}
		sum, err := hashSampleRanges(a.primary.Path, part.Samples)
		if err != nil {
			return err
		}
		seg.ContentSHA256 = sum
		segments = append(segments, seg)
		hashes = append(hashes, sum)
	}
	a.segments = segments
	rn.Report.SegmentHashes = hashes
	payload := transformArtifact{BatchID: rn.BatchID, Range: rn.Range, Segments: segments}
	return rn.materializeCommitted("transform", filepath.Join(rn.WorkDir, "artifacts", "transform.json"), payload)
}

func hashSampleRanges(path string, samples []index.TimedSample) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", clock.Wrap(clock.CategoryIO, path, 0, 0, "open samples for hashing", err)
	}
	defer f.Close()
	h := sha256.New()
	for _, s := range samples {
		var meta [16]byte
		binary.BigEndian.PutUint64(meta[0:8], uint64(s.Offset))
		binary.BigEndian.PutUint32(meta[8:12], s.Size)
		binary.BigEndian.PutUint32(meta[12:16], uint32(s.Number))
		h.Write(meta[:])
		buf := make([]byte, s.Size)
		if _, err := f.ReadAt(buf, s.Offset); err != nil && err != io.EOF {
			return "", clock.Wrap(clock.CategoryIO, path, s.Number, s.Offset, "read sample byte range", err)
		}
		h.Write(buf)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
