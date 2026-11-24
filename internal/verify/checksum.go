// Package verify verifies delivered frame payloads against their manifest
// metadata: every frame must exist, match its declared length and reproduce
// its declared SHA-256 digest, and timestamps must never regress. The first
// corruption is reported with track id, frame index, byte offset and a
// classified error type; verification is streaming and window-bounded.
package verify

import (
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"

	"mediapipe/internal/clock"
	"mediapipe/internal/manifest"
	"mediapipe/internal/model"
)

// FrameOpener resolves a frame's payload as a readable stream. The default
// filesystem opener treats Frame.Name relative to the track directory.
type FrameOpener func(trackID, name string) (io.ReadCloser, error)

// FSFrameOpener returns an opener rooted at baseDir/<trackID>/.
func FSFrameOpener(baseDir string) FrameOpener {
	return func(trackID, name string) (io.ReadCloser, error) {
		return os.Open(filepath.Join(baseDir, trackID, name))
	}
}

// Report is the verification outcome for one track.
type Report struct {
	TrackID    string            `json:"track_id"`
	FrameCount int               `json:"frame_count"`
	BytesRead  int64             `json:"bytes_read"`
	OK         bool              `json:"ok"`
	FirstError *model.MediaError `json:"first_error,omitempty"`
}

// Result aggregates all tracks. CorruptFirstFrame is -1 when every track is
// clean; otherwise it holds the frame index of the first failure and
// CorruptOffset its byte offset within that frame's payload.
type Result struct {
	Reports           []Report `json:"reports"`
	FrameCount        int      `json:"frame_count"`
	CorruptFirstFrame int      `json:"corrupt_first_frame"`
	CorruptOffset     int64    `json:"corrupt_offset_bytes"`
	CorruptTrackID    string   `json:"corrupt_track_id,omitempty"`
}

// Track verifies one mapped track. Reading is buffered through the hash and
// stops at the declared length plus one byte so a truncated frame is caught
// before hashing and an over-long frame cannot grow memory.
func Track(mt *manifest.MappedTrack, open FrameOpener) Report {
	rep := Report{TrackID: mt.Track.ID, FrameCount: len(mt.Frames), OK: true}
	var prevAt clock.Ticks
	for i, mf := range mt.Frames {
		f := mf.Frame
		if i > 0 && mf.At90k < prevAt {
			rep.fail(model.NewError(model.ErrTimestampBack, mt.Track.ID, i, 0,
				"mapped timestamp regressed"))
			return rep
		}
		prevAt = mf.At90k

		rc, err := open(mt.Track.ID, f.Name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				rep.fail(model.NewError(model.ErrMissingFrame, mt.Track.ID, i, 0, f.Name))
			} else {
				rep.fail(model.NewError(model.ErrIO, mt.Track.ID, i, 0, err.Error()))
			}
			return rep
		}
		offset, ok := verifyPayload(rc, f, mt.Track.ID, i, &rep)
		_ = rc.Close()
		if !ok {
			return rep
		}
		rep.BytesRead += offset
	}
	return rep
}

// verifyPayload hashes at most Length+1 bytes. It returns the bytes consumed
// and ok=false after recording the first classified failure.
func verifyPayload(rc io.Reader, f model.Frame, trackID string, idx int, rep *Report) (int64, bool) {
	h := sha256.New()
	// One extra byte reveals truncation vs over-length in a single pass.
	limit := f.Length + 1
	n, err := io.Copy(h, io.LimitReader(rc, limit))
	if err != nil {
		rep.fail(model.NewError(model.ErrIO, trackID, idx, n, err.Error()))
		return n, false
	}
	if n < f.Length {
		rep.fail(model.NewError(model.ErrTruncatedFrame, trackID, idx, n,
			"declared length exceeds payload"))
		return n, false
	}
	if n > f.Length {
		rep.fail(model.NewError(model.ErrTruncatedFrame, trackID, idx, f.Length,
			"payload exceeds declared length"))
		return n, false
	}
	var got model.Hash
	copy(got[:], h.Sum(nil))
	if got != f.SHA256 {
		rep.fail(model.NewError(model.ErrHashMismatch, trackID, idx, n,
			"sha256 "+got.String()))
		return n, false
	}
	return n, true
}

func (r *Report) fail(e *model.MediaError) {
	r.OK = false
	r.FirstError = e
}

// All verifies every track in track order and fills the aggregate pointers
// used by the demo summary. Independent tracks are still attempted after one
// failure so parallel work is not lost.
func All(tracks []*manifest.MappedTrack, open FrameOpener) Result {
	res := Result{CorruptFirstFrame: -1}
	for _, mt := range tracks {
		rep := Track(mt, open)
		res.FrameCount += rep.FrameCount
		res.Reports = append(res.Reports, rep)
		if !rep.OK && res.CorruptFirstFrame == -1 {
			res.CorruptFirstFrame = rep.FirstError.FrameIndex
			res.CorruptOffset = rep.FirstError.Offset
			res.CorruptTrackID = rep.TrackID
		}
	}
	return res
}
