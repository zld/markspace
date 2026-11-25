// Package fixture generates deterministic in-memory media fixtures. No sample
// media is read from disk, which keeps tests and selftest self-contained.
package fixture

import (
	"fmt"
	"math/rand"

	"medpipe/internal/clock"
	"medpipe/internal/ingest"
)

// Fixture is the generated selftest input and its known record boundaries.
type Fixture struct {
	Data []byte

	// Canonical are valid frames used for strict field round-trip checks.
	Canonical []*ingest.Frame
	// OrderedRecords are encoded Canonical records in canonical order.
	OrderedRecords [][]byte

	DuplicateOffset int64
	CorruptOffset   int64
	RecoveryOffset  int64
	TruncateOffset  int64
	TruncateCut     int
}

// Generate builds the complete deterministic selftest stream.
func Generate(seed int64) (*Fixture, error) {
	const w, h = 16, 12
	canonical := []*ingest.Frame{
		newFrame(seed, 1, 0, 0, clock.RTP90K, true, w, h),
		newFrame(seed, 1, 1, 3000, clock.RTP90K, false, w, h),
		newFrame(seed, 1, 2, 6000, clock.RTP90K, true, w, h),
		newFrame(seed, 2, 0, 0, clock.PTS48K, true, w, h),
		newFrame(seed, 2, 1, 1600, clock.PTS48K, false, w, h),
		newFrame(seed, 2, 2, 3200, clock.PTS48K, true, w, h),
	}
	records := make([][]byte, len(canonical))
	for i, f := range canonical {
		b, err := ingest.EncodeFrame(f)
		if err != nil {
			return nil, err
		}
		records[i] = b
	}

	fx := &Fixture{Canonical: canonical, OrderedRecords: records, TruncateCut: 7}
	var data []byte
	appendRecord := func(b []byte) int64 {
		off := int64(len(data))
		data = append(data, b...)
		return off
	}

	// Deliberately reverse-ish order across and within tracks.
	for _, idx := range []int{2, 5, 1, 4, 0, 3} {
		appendRecord(records[idx])
	}

	// Exact duplicate of track-1 PTS 0; the earlier offset must win.
	fx.DuplicateOffset = appendRecord(records[0])

	// Corrupt a payload byte while leaving framing length intact.
	corrupt := append([]byte(nil), records[1]...)
	corrupt[len(corrupt)-1] ^= 0x5a
	fx.CorruptOffset = appendRecord(corrupt)

	// A new unique valid frame proves processing recovered after corruption.
	recovery := newFrame(seed, 3, 9, 9000, clock.RTP90K, true, w, h)
	recoveryBytes, err := ingest.EncodeFrame(recovery)
	if err != nil {
		return nil, err
	}
	fx.RecoveryOffset = appendRecord(recoveryBytes)

	// A boundary-truncated final record. No read may go past the input slice.
	truncated := append([]byte(nil), records[2]...)
	if fx.TruncateCut >= len(truncated) {
		return nil, fmt.Errorf("truncate cut %d exceeds record %d", fx.TruncateCut, len(truncated))
	}
	fx.TruncateOffset = int64(len(data))
	data = append(data, truncated[:len(truncated)-fx.TruncateCut]...)

	fx.Data = data
	return fx, nil
}

func newFrame(seed int64, track uint16, salt int, pts int64, tb clock.Timebase, key bool, w, h int) *ingest.Frame {
	r := rand.New(rand.NewSource(seed + int64(track)*1009 + int64(salt)*9176))
	pixels := make([]byte, w*h*3)
	r.Read(pixels)
	// Add deterministic structure so repeated seeds and frame positions differ.
	for i := range pixels {
		pixels[i] ^= byte((seed >> uint(i%24)) + int64(i*17) + int64(track)*37)
	}
	return &ingest.Frame{
		TrackID: track, PTS: pts, Timebase: tb, Width: w, Height: h,
		KeyFrame: key, PixelFormat: ingest.PixelRGB24,
		Color:  ingest.ColorMetadata{Primaries: 1, Transfer: 13, Matrix: 1, FullRange: true},
		Pixels: pixels,
	}
}
