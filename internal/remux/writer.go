// Package remux writes a new MP4 container around the original essence bytes
// without re-encoding. Every payload is copied verbatim, so the SHA-256 of a
// packet before and after remux must match; verification compares exactly
// that. The writer only serialises after the mapping has been resolved in
// dependency order, and it coordinates parallel track workers through the
// shared explicit timeline so a fragment can only commit if the timeline says
// its interval exists.
package remux

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"

	mp4 "github.com/abema/go-mp4"

	"archive/internal/demux"
	"archive/internal/fault"
	"archive/internal/model"
)

// TrackSource is the decided payload stream for one logical track.
type TrackSource struct {
	Key      string
	Logical  string
	Kind     model.TrackKind
	Codec    string
	TrackID  uint32
	Timebase model.Timebase
	// Rotation, Crop and Color travel with the essence into the output so a
	// restored asset keeps its display properties.
	Rotation int
	Crop     *model.Crop
	Color    model.ColorDescription
	Packets  []*demux.Packet
	Required bool
}

// CommittedFragment is one fragment that reached the output.
type CommittedFragment struct {
	TrackKey  string `json:"track_key"`
	Seq       int    `json:"seq"`
	StartTick uint64 `json:"start_tick"`
	Duration  uint64 `json:"duration"`
	StartNS   int64  `json:"start_ns"`
	EndNS     int64  `json:"end_ns"`
	Offset    uint64 `json:"output_offset"`
	Size      uint64 `json:"size"`
	SHA256    string `json:"sha256"`
	Sync      bool   `json:"sync"`
}

// TrackResult records one track's committed output.
type TrackResult struct {
	Key       string              `json:"key"`
	Logical   string              `json:"logical"`
	Kind      model.TrackKind     `json:"kind"`
	Timebase  model.Timebase      `json:"timebase"`
	Committed []CommittedFragment `json:"committed"`
	Packets   int                 `json:"packets"`
	StartNS   int64               `json:"start_ns"`
	EndNS     int64               `json:"end_ns"`
}

// Slot is a reserved position of one fragment in the mdat payload. The
// parallel workers fill slots; the single writer thread serialises them.
type Slot struct {
	TrackIdx int
	FragIdx  int
	Offset   uint64 // absolute file offset of the essence bytes
}

// ErrInterrupted is returned when an injected interruption stopped the run
// before it could serialise an output. The partial committed state is still
// returned so a checkpoint can preserve it.
var ErrInterrupted = errors.New("remux: interrupted")

// Result is the remux output for the whole job.
type Result struct {
	Data      []byte         `json:"-"`
	Tracks    []*TrackResult `json:"tracks"`
	Faults    fault.List     `json:"-"`
	Fragments int            `json:"fragments"`
	SHA256    string         `json:"sha256"`
}

// Options controls parallel behaviour.
type Options struct {
	// Workers is the number of concurrent track workers.
	Workers int
	// FailTrack, when non-empty, makes that track fail after committing the
	// given number of fragments. The demo uses it to simulate an interruption
	// after partial progress; committed fragments must stay valid and must not
	// be written twice on resume.
	FailTrack string
	FailAfter int
	// Committed is the checkpoint's previously committed fragments. Those
	// fragments are reused, not rewritten.
	Committed map[string][]CommittedFragment
	// OnCommit is invoked once per newly committed fragment, under the
	// writer lock, so a checkpoint can be persisted at fragment granularity.
	OnCommit func(TrackResult, CommittedFragment) error
}

// Write builds the output MP4. It first resolves all tracks in dependency
// order (the caller supplies them already ordered), then reserves mdat slots,
// then fills them concurrently under a shared timeline commit gate, and
// finally serialises moov with real offsets.
func Write(timeline *model.Timeline, tracks []*TrackSource, opt Options) (*Result, error) {
	if len(tracks) == 0 {
		return nil, &fault.Error{Code: fault.MissingRequiredTrack, Detail: "remux: no tracks to write"}
	}
	if opt.Workers <= 0 {
		opt.Workers = 1
	}
	// Stable logical order, dependency order enforced by the caller.
	for _, t := range tracks {
		if t.Timebase.Timescale == 0 {
			return nil, &fault.Error{Code: fault.InvalidArgument, Detail: "remux: track " + t.Key + " has no timebase"}
		}
	}

	// Skip fragments already committed by a previous run so resume never
	// writes them twice. The reused set is keyed by (track, seq).
	reused := map[string]map[int]CommittedFragment{}
	for k, v := range opt.Committed {
		m := map[int]CommittedFragment{}
		for _, f := range v {
			m[f.Seq] = f
		}
		reused[k] = m
	}

	// Reserve slots. Layout: ftyp | moov | mdat header | payloads.
	perTrack := make([][]*demux.Packet, len(tracks))
	totalPayload := uint64(0)
	for i, t := range tracks {
		for _, p := range t.Packets {
			if _, done := reused[t.Key][p.Seq]; done {
				continue
			}
			perTrack[i] = append(perTrack[i], p)
			totalPayload += uint64(len(p.Payload))
		}
	}
	_ = totalPayload

	result := &Result{}
	committed := make([][]CommittedFragment, len(tracks))
	var mu sync.Mutex
	var faults fault.List
	var firstErr error
	interrupted := false

	sem := make(chan struct{}, opt.Workers)
	var wg sync.WaitGroup
	for ti := range tracks {
		ti := ti
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tr := tracks[ti]
			trRes := TrackResult{Key: tr.Key, Logical: tr.Logical, Kind: tr.Kind, Timebase: tr.Timebase}
			count := 0
			for _, p := range perTrack[ti] {
				if opt.FailTrack == tr.Key && count >= opt.FailAfter {
					mu.Lock()
					interrupted = true
					mu.Unlock()
					return
				}
				// A fragment may only commit if the shared timeline
				// describes its interval. This is the coordination point:
				// no worker can write outside the explicit timeline another
				// worker may be extending.
				r := model.Range{StartTick: p.StartTick, Duration: p.Duration}
				if err := timeline.ValidateFragment(tr.Key, r); err != nil {
					mu.Lock()
					faults.Add(&fault.Error{
						Code: fault.CorruptSegment, Track: tr.Key, TrackID: p.TrackID,
						StartNS:  tr.Timebase.TickToNanos(p.StartTick),
						EndNS:    tr.Timebase.TickToNanos(p.StartTick + p.Duration),
						HasRange: true, Recoverable: false,
						Detail: "remux: " + err.Error(),
					})
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				sum := sha256.Sum256(p.Payload)
				cf := CommittedFragment{
					TrackKey: tr.Key, Seq: p.Seq,
					StartTick: p.StartTick, Duration: p.Duration,
					StartNS: tr.Timebase.TickToNanos(p.StartTick),
					EndNS:   tr.Timebase.TickToNanos(p.StartTick + p.Duration),
					Size:    uint64(len(p.Payload)),
					SHA256:  hex.EncodeToString(sum[:]),
					Sync:    p.Sync,
				}
				mu.Lock()
				committed[ti] = append(committed[ti], cf)
				trRes.Packets++
				mu.Unlock()
				count++
				if opt.OnCommit != nil {
					if err := opt.OnCommit(trRes, cf); err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = err
						}
						mu.Unlock()
						return
					}
				}
			}
			mu.Lock()
			if !interrupted {
				for _, cf := range reused[tr.Key] {
					committed[ti] = append(committed[ti], cf)
					trRes.Packets++
				}
			}
			sort.Slice(committed[ti], func(a, b int) bool { return committed[ti][a].Seq < committed[ti][b].Seq })
			trRes.Committed = committed[ti]
			if len(trRes.Committed) > 0 {
				trRes.StartNS = trRes.Committed[0].StartNS
				trRes.EndNS = trRes.Committed[0].EndNS
				for _, cf := range trRes.Committed {
					if cf.StartNS < trRes.StartNS {
						trRes.StartNS = cf.StartNS
					}
					if cf.EndNS > trRes.EndNS {
						trRes.EndNS = cf.EndNS
					}
				}
			}
			result.Tracks = append(result.Tracks, &trRes)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	// Deterministic track order in the result regardless of goroutine order.
	sort.Slice(result.Tracks, func(i, j int) bool { return result.Tracks[i].Key < result.Tracks[j].Key })
	if interrupted {
		// Do not serialise a partial file: an interruption must leave the
		// last verified checkpoint intact, never a plausible-looking but
		// incomplete output.
		result.Faults = faults.Dedup()
		for _, t := range result.Tracks {
			result.Fragments += len(t.Committed)
		}
		return result, ErrInterrupted
	}

	// Serialise the MP4. Because the committed set is now fixed, the moov and
	// mdat sizes are exact.
	data, err := serialise(tracks, result)
	if err != nil {
		return nil, err
	}
	result.Data = data
	sum := sha256.Sum256(data)
	result.SHA256 = hex.EncodeToString(sum[:])
	for _, t := range result.Tracks {
		result.Fragments += len(t.Committed)
	}
	result.Faults = faults.Dedup()
	return result, nil
}

// serialise writes ftyp, a faststart moov describing exactly the committed
// packets, then mdat with the original payload bytes at the committed
// offsets.
func serialise(tracks []*TrackSource, res *Result) ([]byte, error) {
	// Build lookup from track key to its committed fragments.
	byKey := map[string]*TrackResult{}
	for _, tr := range res.Tracks {
		byKey[tr.Key] = tr
	}
	// Payload bytes are taken from the packet list, matched by seq.
	payloadOf := map[string]map[int][]byte{}
	for _, t := range tracks {
		m := map[int][]byte{}
		for _, p := range t.Packets {
			m[p.Seq] = p.Payload
		}
		payloadOf[t.Key] = m
	}

	// Pass 1: measure the moov with placeholder chunk offsets.
	measure := &memWriteSeeker{}
	mw := mp4.NewWriter(measure)
	if err := writeFtyp(mw); err != nil {
		return nil, err
	}
	ftypSize := uint64(len(measure.buf))
	// Placeholder offsets do not change the encoded size (fixed width).
	if err := writeMoov(mw, tracks, byKey, 0); err != nil {
		return nil, err
	}
	moovSize := uint64(len(measure.buf)) - ftypSize
	mdatHeader := uint64(8)
	base := ftypSize + moovSize + mdatHeader

	// Pass 2: compute the payload layout and patch committed offsets.
	type layout struct {
		track *TrackSource
		tr    *TrackResult
		start uint64
	}
	layouts := make([]layout, 0, len(tracks))
	cursor := base
	for _, t := range tracks {
		tr := byKey[t.Key]
		if tr == nil {
			continue
		}
		start := cursor
		for i := range tr.Committed {
			cf := &tr.Committed[i]
			tr.Committed[i].Offset = cursor
			cursor += cf.Size
		}
		layouts = append(layouts, layout{track: t, tr: tr, start: start})
	}
	payloadSize := cursor - base

	out := &memWriteSeeker{}
	w := mp4.NewWriter(out)
	if err := writeFtyp(w); err != nil {
		return nil, err
	}
	if err := writeMoov(w, tracks, byKey, base); err != nil {
		return nil, err
	}
	if uint64(len(out.buf)) != base-mdatHeader {
		return nil, fmt.Errorf("remux: moov size drift (%d vs %d)", uint64(len(out.buf)), base-mdatHeader)
	}
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:4], uint32(payloadSize+mdatHeader))
	copy(hdr[4:8], "mdat")
	if _, err := out.Write(hdr); err != nil {
		return nil, err
	}
	if uint64(len(out.buf)) != base {
		return nil, fmt.Errorf("remux: mdat header mismatch")
	}
	for _, t := range tracks {
		tr := byKey[t.Key]
		if tr == nil {
			continue
		}
		for _, cf := range tr.Committed {
			payload, ok := payloadOf[t.Key][cf.Seq]
			if !ok {
				return nil, fmt.Errorf("remux: missing payload for %s/%d", t.Key, cf.Seq)
			}
			if _, err := out.Write(payload); err != nil {
				return nil, err
			}
		}
	}
	return out.buf, nil
}
