package gen

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"mediad/internal/mp4"
	"mediad/internal/timeline"
)

// hashPayload hashes the concatenated sample bytes of a fragment's first track.
func hashPayload(data []byte, f *mp4.Fragment) string {
	h := sha256.New()
	for _, s := range f.Tracks[0].Samples {
		h.Write(data[s.DataOffset : s.DataOffset+s.Size])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func decodeAll(t *testing.T, data []byte) []*mp4.Fragment {
	t.Helper()
	r := mp4.NewSeeker(data)
	boxes, err := mp4.ScanTopLevel(r, "gen.m4s", mp4.Limits{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var frags []*mp4.Fragment
	for i := 0; i < len(boxes); i++ {
		if boxes[i].Type != "moof" {
			continue
		}
		var mdat *mp4.Box
		if i+1 < len(boxes) && boxes[i+1].Type == "mdat" {
			mdat = boxes[i+1]
		}
		f, err := mp4.DecodeFragment(r, "gen.m4s", boxes[i], mdat, mp4.Limits{})
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		frags = append(frags, f)
	}
	return frags
}

func TestNormalGroupDecodes(t *testing.T) {
	data := NormalGroup()
	frags := decodeAll(t, data)
	if len(frags) != 3 {
		t.Fatalf("want 3 fragments, got %d", len(frags))
	}
	// Every sample range must lie inside its mdat.
	for _, f := range frags {
		for _, tf := range f.Tracks {
			for _, s := range tf.Samples {
				if s.DataOffset < 0 || s.DataOffset+s.Size > int64(len(data)) {
					t.Fatalf("sample escapes file: %+v", s)
				}
			}
		}
	}
	// Fragment 1 and 3 carry identical loud payloads.
	h1 := hashPayload(data, frags[0])
	h3 := hashPayload(data, frags[2])
	if h1 != h3 {
		t.Fatalf("duplicate content must match: %s vs %s", h1, h3)
	}
}

func TestBoundaryGroupVFRAndRollover(t *testing.T) {
	data := BoundaryGroup()
	frags := decodeAll(t, data)
	if len(frags) != 3 {
		t.Fatalf("want 3 boundary fragments, got %d", len(frags))
	}
	// VFR: durations within fragment 1 differ.
	d0 := frags[0].Tracks[0].Samples[0].Duration
	d1 := frags[0].Tracks[0].Samples[1].Duration
	if d0 == d1 {
		t.Fatal("VFR requires unequal sample durations")
	}
	// Fragment 2 base is the wrapped continuation near zero.
	if frags[1].Tracks[0].BaseMediaT >= 0x1000 {
		t.Fatalf("wrapped tfdt should be small, got %d", frags[1].Tracks[0].BaseMediaT)
	}
}

func TestTruncatedGroupLocatesCut(t *testing.T) {
	data, cutOffset := TruncatedGroup()
	r := mp4.NewSeeker(data)
	_, err := mp4.ScanTopLevel(r, "cut.m4s", mp4.Limits{})
	var me *timeline.MediaError
	if !asMedia(err, &me) {
		t.Fatalf("truncated input must error, got %v", err)
	}
	if me.Code != timeline.ErrBoxTruncated {
		t.Fatalf("want E_BOX_TRUNCATED, got %s", me.Code)
	}
	diff := me.Offset - cutOffset
	if diff < 0 {
		diff = -diff
	}
	if diff > 1 {
		t.Fatalf("cut located at %d, actual %d (±1 required)", me.Offset, cutOffset)
	}
}

func asMedia(err error, target **timeline.MediaError) bool {
	if err == nil {
		return false
	}
	me := timeline.AsMediaError(err)
	*target = me
	return me != nil
}
