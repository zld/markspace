// Package ingest validates media segments against their manifest references,
// detects truncation and checksum corruption, retries recoverable failures,
// and emits a recovery record for every segment so restoration is auditable.
package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediapipe/internal/clock"
	"mediapipe/internal/manifest"
)

// Hard limits from the pipeline constraints.
const (
	MaxSegmentBytes   = 64 << 20 // 64 MiB per segment
	MaxTrackDuration  = clock.Ticks(30 * 60 * clock.Rate)
	DefaultMaxRetries = 2
)

// Status of one segment after ingest.
type Status int

const (
	OK Status = iota
	Recovered
	Unrecoverable
	Missing
)

func (s Status) String() string {
	switch s {
	case OK:
		return "ok"
	case Recovered:
		return "recovered"
	case Unrecoverable:
		return "unrecoverable"
	case Missing:
		return "missing"
	}
	return "unknown"
}

// Record is the auditable outcome of ingesting one segment slot.
type Record struct {
	Ref      manifest.SegmentRef
	Status   Status
	Attempts int
	Reason   string
	Payload  []byte // PCM s16le interleaved, present when OK or Recovered
}

// Source fetches raw segment bytes by playlist-relative URI.
type Source interface {
	ReadSegment(uri string) ([]byte, error)
}

// Validator checks segments and produces recovery records.
type Validator struct {
	Src        Source
	Channels   int                 // expected interleaved channels
	InRate     int                 // source sample rate (defaults to clock.Rate)
	MaxRetries int                 // retries after the first attempt
	Checksums  map[string][32]byte // uri -> expected payload hash (optional)
}

// Ingest fetches and validates one segment reference. It never returns a
// nil record: every slot ends up OK, Recovered, Unrecoverable or Missing.
func (v *Validator) Ingest(ctx context.Context, ref manifest.SegmentRef) (rec Record, err error) {
	rec.Ref = ref
	if ref.Missing {
		rec.Status = Missing
		rec.Reason = "manifest gap"
		return rec, nil
	}
	retries := v.MaxRetries
	if retries < 0 {
		retries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		rec.Attempts = attempt + 1
		if err := ctx.Err(); err != nil {
			return rec, &clock.StageError{Track: ref.TrackID, Stage: clock.StageIngest, Op: "cancel", Err: err}
		}
		lastErr = v.tryOnce(ref, &rec)
		if lastErr == nil {
			if attempt > 0 {
				rec.Status = Recovered
				rec.Reason = fmt.Sprintf("recovered after %d retries", attempt)
			} else {
				rec.Status = OK
			}
			return rec, nil
		}
	}
	rec.Status = Unrecoverable
	rec.Reason = lastErr.Error()
	rec.Payload = nil
	return rec, nil
}

// tryOnce performs a single fetch+validate pass.
func (v *Validator) tryOnce(ref manifest.SegmentRef, rec *Record) error {
	raw, err := v.Src.ReadSegment(ref.URI)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if len(raw) == 0 {
		return fmt.Errorf("empty segment")
	}
	if len(raw) > MaxSegmentBytes {
		return fmt.Errorf("segment exceeds 64 MiB limit: %d bytes", len(raw))
	}
	payload, err := extractPayload(raw)
	if err != nil {
		return err // truncation / structural corruption
	}
	inRate := v.InRate
	if inRate <= 0 {
		inRate = clock.Rate
	}
	want := int(ref.Interval.Dur()) * inRate / clock.Rate * v.Channels * 2
	if len(payload) != want {
		return fmt.Errorf("payload length %d, want %d for %d ticks x %d ch @ %d Hz",
			len(payload), want, ref.Interval.Dur(), v.Channels, inRate)
	}
	if sum, ok := v.Checksums[ref.URI]; ok {
		got := sha256.Sum256(payload)
		if got != sum {
			return fmt.Errorf("checksum mismatch for %s", ref.URI)
		}
	}
	rec.Payload = payload
	return nil
}

// extractPayload walks the MP4 box structure with go-mp4 and returns the
// bytes of the first `free` box, which carries the PCM payload in this
// pipeline's segment format. A truncated tail box makes the walk fail,
// which is how truncation is detected.
func extractPayload(raw []byte) ([]byte, error) {
	var payload []byte
	var sawFtyp bool
	_, err := mp4.ReadBoxStructure(bytes.NewReader(raw), func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeFtyp():
			sawFtyp = true
			return nil, nil
		case mp4.BoxTypeFree():
			if payload != nil {
				return nil, fmt.Errorf("multiple payload boxes")
			}
			var buf bytes.Buffer
			if _, err := h.ReadData(&buf); err != nil {
				return nil, fmt.Errorf("read payload: %w", err)
			}
			payload = buf.Bytes()
			return nil, nil
		}
		return nil, nil
	})
	if err != nil {
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			return nil, fmt.Errorf("truncated segment: %w", err)
		}
		return nil, fmt.Errorf("box structure: %w", err)
	}
	if !sawFtyp {
		return nil, fmt.Errorf("missing ftyp box")
	}
	if payload == nil {
		return nil, fmt.Errorf("missing payload box")
	}
	return payload, nil
}
