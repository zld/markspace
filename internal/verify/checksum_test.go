package verify

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"testing"

	"mediapipe/internal/manifest"
	"mediapipe/internal/model"
)

func digest(b []byte) model.Hash {
	sum := sha256.Sum256(b)
	var h model.Hash
	copy(h[:], sum[:])
	return h
}

// memOpener serves synthetic payloads from memory; missing names yield a
// not-found error, exercising the missing-frame branch without disk files.
func memOpener(payloads map[string][]byte, missing map[string]bool) FrameOpener {
	return func(trackID, name string) (io.ReadCloser, error) {
		key := trackID + "/" + name
		if missing[key] {
			return nil, fmt.Errorf("open %s: %w", key, os.ErrNotExist)
		}
		b, ok := payloads[key]
		if !ok {
			return nil, fmt.Errorf("open %s: %w", key, os.ErrNotExist)
		}
		return io.NopCloser(bytes.NewReader(b)), nil
	}
}

func buildTrack(t *testing.T, id string, payloads [][]byte) (*manifest.MappedTrack, map[string][]byte) {
	t.Helper()
	tr := &model.Track{ID: id, Kind: model.KindVideo, FPS: 30}
	store := map[string][]byte{}
	mt := &manifest.MappedTrack{Track: tr}
	for i, p := range payloads {
		name := fmt.Sprintf("f%03d.raw", i)
		store[id+"/"+name] = p
		tr.Frames = append(tr.Frames, model.Frame{
			TrackID: id, Index: i, Name: name, Length: int64(len(p)),
			SHA256: digest(p), TimeUnits: int64(i) * 3000, TimeRate: 90000,
		})
		mt.Frames = append(mt.Frames, manifest.MappedFrame{
			Frame: tr.Frames[i], At90k: 3000,
		})
	}
	return mt, store
}

func TestHealthyTrack(t *testing.T) {
	payloads := [][]byte{bytes.Repeat([]byte{0xAB}, 128), bytes.Repeat([]byte{0xCD}, 256)}
	mt, store := buildTrack(t, "vid", payloads)
	rep := Track(mt, memOpener(store, nil))
	if !rep.OK || rep.BytesRead != 384 || rep.FrameCount != 2 {
		t.Fatalf("healthy track rejected: %+v", rep)
	}
}

func TestCorruptionTypesAndFirstOffset(t *testing.T) {
	good := bytes.Repeat([]byte{1}, 64)
	truncated := good[:40]               // shorter than declared length
	wrong := bytes.Repeat([]byte{2}, 64) // same length, different digest
	overlong := append(append([]byte{}, good...), 9)

	// Each case builds a two-frame track whose second frame declares the
	// hash of good but is delivered with the case's payload.
	cases := []struct {
		name      string
		payload   []byte
		want      model.ErrorType
		wantOff   int64
		missingIt bool
	}{
		{"truncated", truncated, model.ErrTruncatedFrame, 40, false},
		{"hash mismatch", wrong, model.ErrHashMismatch, 64, false},
		{"overlong", overlong, model.ErrTruncatedFrame, 64, false},
		{"missing", good, model.ErrMissingFrame, 0, true},
	}
	for _, c := range cases {
		mt, store := buildTrack(t, "vid", [][]byte{good, good})
		// Replace the delivered second payload while the manifest keeps
		// declaring the original (good) hash and length.
		key := "vid/" + mt.Frames[1].Frame.Name
		store[key] = c.payload
		missing := map[string]bool{}
		if c.missingIt {
			delete(store, key)
			missing[key] = true
		}
		rep := Track(mt, memOpener(store, missing))
		if rep.OK || rep.FirstError == nil {
			t.Errorf("%s: expected failure", c.name)
			continue
		}
		if rep.FirstError.Type != c.want || rep.FirstError.FrameIndex != 1 || rep.FirstError.Offset != c.wantOff {
			t.Errorf("%s: got %+v, want type=%s frame=1 offset=%d",
				c.name, rep.FirstError, c.want, c.wantOff)
		}
	}
}

func TestAllStopsAtFirstButContinuesTracks(t *testing.T) {
	good := bytes.Repeat([]byte{7}, 32)
	bad := bytes.Repeat([]byte{8}, 32)
	mt1, store1 := buildTrack(t, "vid-a", [][]byte{good, good})
	// Deliver bad bytes under frame 1 while its manifest hash stays "good".
	store1["vid-a/"+mt1.Frames[1].Frame.Name] = bad
	mt2, store2 := buildTrack(t, "vid-b", [][]byte{good, good})
	store := map[string][]byte{}
	for k, v := range store1 {
		store[k] = v
	}
	for k, v := range store2 {
		store[k] = v
	}
	res := All([]*manifest.MappedTrack{mt1, mt2}, memOpener(store, nil))
	if res.CorruptFirstFrame != 1 || res.CorruptOffset != 32 || res.CorruptTrackID != "vid-a" {
		t.Fatalf("aggregate = %+v", res)
	}
	if len(res.Reports) != 2 || !res.Reports[1].OK {
		t.Fatal("independent track must keep verifying after a failure")
	}
}

func TestTimestampRegressionReported(t *testing.T) {
	mt, store := buildTrack(t, "vid", [][]byte{{1}, {2}})
	mt.Frames[1].At90k = mt.Frames[0].At90k - 1
	rep := Track(mt, memOpener(store, nil))
	if rep.OK || rep.FirstError.Type != model.ErrTimestampBack || rep.FirstError.FrameIndex != 1 {
		t.Fatalf("regression not detected: %+v", rep.FirstError)
	}
}
