// Package fingerprint computes content-addressed fingerprints over fixed
// time windows of a track. The fingerprint identifier is the hex SHA-256 of
// the window's payload bytes, so identical content produced on any rerun
// resolves to the same identifier and is reused, while different content can
// never be merged into one fingerprint.
package fingerprint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"

	"mediad/ts"
)

// Fingerprint is one content window's identity and placement.
type Fingerprint struct {
	// ID is the deterministic, content-derived identifier (hex SHA-256).
	ID string
	// ContentHash is the same value as ID, kept explicit for manifests.
	ContentHash string
	TrackID     string
	WindowIndex int
	Start       int64
	End         int64
	Bytes       int
	// Reused is true when an identical-content fingerprint already existed,
	// i.e. this window was deduplicated against a prior compute/rerun.
	Reused bool
}

// Fingerprinter accumulates events into fixed-width windows per track.
type Fingerprinter struct {
	windowTicks int64
	// cache is the content-addressed store: hash -> first fingerprint.
	cache map[string]Fingerprint
	out   []Fingerprint
	// open holds the in-progress window state per track.
	open map[string]*window
}

type window struct {
	trackID string
	index   int
	start   int64
	end     int64
	hasher  hash.Hash
	bytes   int
}

// NewFingerprinter requires a positive window width in 90 kHz ticks.
func NewFingerprinter(windowTicks int64) (*Fingerprinter, error) {
	if windowTicks <= 0 {
		return nil, fmt.Errorf("fingerprint: window width must be positive, got %d", windowTicks)
	}
	return &Fingerprinter{
		windowTicks: windowTicks,
		cache:       map[string]Fingerprint{},
		open:        map[string]*window{},
	}, nil
}

// Add folds one event into its track's current window, closing a window when
// the event crosses the next boundary.
func (f *Fingerprinter) Add(ev ts.TrackEvent) {
	w, ok := f.open[ev.TrackID]
	if !ok {
		w = f.newWindow(ev.TrackID, ev.Start)
		f.open[ev.TrackID] = w
	}
	// Close windows whose nominal boundary the event passes.
	for ev.Start >= w.start+f.windowTicks {
		f.close(w)
		w = f.newWindow(ev.TrackID, w.start+f.windowTicks)
		f.open[ev.TrackID] = w
	}
	f.writePayload(w, ev.Payload)
	w.bytes += len(ev.Payload)
	w.end = ev.End
}

func (f *Fingerprinter) newWindow(track string, start int64) *window {
	return &window{trackID: track, index: int(start / f.windowTicks), start: start, hasher: sha256.New()}
}

// writePayload feeds a length-prefixed payload so that adjacent payloads
// cannot collide by mere concatenation (e.g. ["ab","c"] vs ["a","bc"]).
func (f *Fingerprinter) writePayload(w *window, payload []byte) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(payload)))
	_, _ = w.hasher.Write(lenBuf[:])
	_, _ = w.hasher.Write(payload)
}

func (f *Fingerprinter) close(w *window) {
	sum := w.hasher.Sum(nil)
	hash := hex.EncodeToString(sum)
	fp := Fingerprint{
		ID: hash, ContentHash: hash, TrackID: w.trackID,
		WindowIndex: w.index, Start: w.start, End: w.end, Bytes: w.bytes,
	}
	if existing, ok := f.cache[hash]; ok {
		fp.Reused = true
		// Reuse the identity but keep this window's own placement.
		fp.ID = existing.ID
	} else {
		f.cache[hash] = fp
	}
	f.out = append(f.out, fp)
}

// Flush closes every open window and returns fingerprints in arrival order.
func (f *Fingerprinter) Flush() []Fingerprint {
	for track, w := range f.open {
		f.close(w)
		delete(f.open, track)
	}
	return f.out
}

// ReusedCount reports how many windows were deduplicated against prior
// content, including across reruns that share the same store directory.
func (f *Fingerprinter) ReusedCount() int {
	n := 0
	for _, fp := range f.out {
		if fp.Reused {
			n++
		}
	}
	return n
}

// Lookup returns the cached fingerprint for a content hash when present.
func (f *Fingerprinter) Lookup(contentHash string) (Fingerprint, bool) {
	fp, ok := f.cache[contentHash]
	return fp, ok
}

// Preload seeds the content-addressed cache with fingerprints produced by a
// previous run, so identical windows are marked Reused rather than recomputed.
// Placement fields of the preloaded entries are not reused; only the
// content->identity mapping matters for idempotency.
func (f *Fingerprinter) Preload(fps []Fingerprint) {
	for _, fp := range fps {
		if _, ok := f.cache[fp.ContentHash]; !ok {
			f.cache[fp.ContentHash] = fp
		}
	}
}
