package mediakernel

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// Fingerprint identifies normalized media content of one track in one time
// window. Identical content always yields the same fingerprint.
type Fingerprint struct {
	Track  uint64 `json:"track"`
	Window int64  `json:"window"`
	Hash   string `json:"hash"` // hex SHA-256
}

// Fingerprinter computes content fingerprints per track and time window and
// deduplicates them: only the first output reference is kept.
type Fingerprinter struct {
	Window     int64 // window size in nanoseconds
	seen       map[string]string
	FPS        []Fingerprint
	Duplicates int
}

func NewFingerprinter(windowNs int64) *Fingerprinter {
	if windowNs <= 0 {
		windowNs = 1_000_000_000
	}
	return &Fingerprinter{Window: windowNs, seen: make(map[string]string)}
}

// WindowIndex maps a timestamp to its fingerprint window.
func (f *Fingerprinter) WindowIndex(ns int64) int64 {
	i := ns / f.Window
	if ns < 0 && ns%f.Window != 0 {
		i--
	}
	return i
}

// HashContent builds the canonical fingerprint input: a versioned,
// length-prefixed encoding of track, window and normalized payload.
func HashContent(track uint64, window int64, payload []byte) string {
	h := sha256.New()
	h.Write([]byte("MKF1"))
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], track)
	h.Write(b[:])
	binary.BigEndian.PutUint64(b[:], uint64(window))
	h.Write(b[:])
	binary.BigEndian.PutUint64(b[:], uint64(len(payload)))
	h.Write(b[:])
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

// Add fingerprints one normalized payload. It returns the content hash and
// whether the content was already seen (duplicate).
func (f *Fingerprinter) Add(track uint64, ns int64, payload []byte) (string, bool) {
	w := f.WindowIndex(ns)
	hash := HashContent(track, w, payload)
	ref := fmt.Sprintf("track=%d/window=%d", track, w)
	if _, ok := f.seen[hash]; ok {
		f.Duplicates++
		return hash, true
	}
	f.seen[hash] = ref
	f.FPS = append(f.FPS, Fingerprint{Track: track, Window: w, Hash: hash})
	return hash, false
}

// FingerprintState is the serializable dedup state for checkpoints.
type FingerprintState struct {
	FPS        []Fingerprint     `json:"fps"`
	Seen       map[string]string `json:"seen"`
	Duplicates int               `json:"duplicates"`
}

func (f *Fingerprinter) Snapshot() FingerprintState {
	return FingerprintState{FPS: f.FPS, Seen: f.seen, Duplicates: f.Duplicates}
}

func (f *Fingerprinter) Restore(st FingerprintState) {
	f.FPS, f.seen, f.Duplicates = st.FPS, st.Seen, st.Duplicates
	if f.seen == nil {
		f.seen = make(map[string]string)
	}
}
