// Package index scans frame streams into a time-ordered, verifiable index and
// caches it bound to the input digest and an index version.
package index

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"

	"mediacore/media"
)

// Version is the on-disk index format version. Bump it whenever the entry
// meaning or digest computation changes; caches with another version are
// rejected and rebuilt.
const Version = 1

// Entry is one indexed frame. It is a value type so entries can be sorted and
// hashed without pointer aliasing.
type Entry struct {
	Offset        int64    `json:"offset"`
	Length        int      `json:"length"`
	Seq           uint32   `json:"seq"`
	RTPTimestamp  uint32   `json:"rtp_timestamp"`
	Ticks         int64    `json:"ticks"`
	CaptureMS     int64    `json:"capture_ms"`
	Width         uint16   `json:"width"`
	Height        uint16   `json:"height"`
	DurationTicks uint32   `json:"duration_ticks"`
	StreamID      uint32   `json:"stream_id"`
	MetaStatus    string   `json:"meta_status"`
	VolumeDBFS    *float64 `json:"volume_dbfs,omitempty"`
	Digest        string   `json:"digest"`
	Wrapped       bool     `json:"wrapped,omitempty"`
	Regressed     bool     `json:"regressed,omitempty"`
}

// SizeBytes is the encoded record size for the entry's frame.
func (e Entry) SizeBytes() int { return e.Length }

// DurationMS converts the nominal duration for reporting when no next frame
// exists.
func (e Entry) DurationMS(timescale uint32) int64 {
	return roundDiv(int64(e.DurationTicks)*1000, int64(timescale))
}

// Issue is a recorded scan problem. Scanning continues where possible, but
// issues always mark the index as incomplete.
type Issue struct {
	Category    media.Category `json:"category"`
	Offset      int64          `json:"offset"`
	Seq         uint32         `json:"seq,omitempty"`
	Message     string         `json:"message"`
	Recoverable bool           `json:"recoverable"`
}

// Index is the scanned, time-sorted view of a frame stream.
type Index struct {
	Version     int     `json:"version"`
	Timescale   uint32  `json:"timescale"`
	InputDigest string  `json:"input_digest"`
	IndexDigest string  `json:"index_digest"`
	EntryCount  int     `json:"entry_count"`
	Complete    bool    `json:"complete"`
	StartTicks  int64   `json:"start_ticks"`
	EndTicks    int64   `json:"end_ticks"`
	Issues      []Issue `json:"issues,omitempty"`
	Entries     []Entry `json:"entries"`
	// Reordered reports whether scanning had to sort entries by media time.
	Reordered bool `json:"reordered"`
}

// Options configures scanning.
type Options struct {
	Timescale    uint32
	Limits       media.Limits
	ClockTolerMS int64
}

// DefaultOptions returns a 90 kHz clock with 10 ms regression tolerance.
func DefaultOptions() Options {
	return Options{
		Timescale:    media.DefaultTimescale,
		Limits:       media.DefaultLimits(),
		ClockTolerMS: 10,
	}
}

// Validate reports option errors.
func (o Options) Validate() error {
	if o.Timescale == 0 {
		return media.New(media.CatInvalidParams, media.StageScan, "timescale must be positive")
	}
	if o.Limits.MaxPayload == 0 {
		return media.New(media.CatInvalidParams, media.StageScan, "payload limit must be positive")
	}
	return nil
}

// Scan reads every frame record from r, validates it, assigns monotonic media
// times, sorts entries by media time, and binds the result to a digest of the
// complete input byte stream.
//
// A damaged record stops the scan: the index is returned with Complete=false,
// the valid prefix of entries, and the located issue. The error is also
// returned so callers cannot mistake a partial index for a full one.
func Scan(r io.Reader, opts Options) (*Index, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	hasher := sha256.New()
	fr := media.NewFrameReader(io.TeeReader(r, hasher), opts.Limits)
	clk, _ := media.NewClock(opts.Timescale)
	clk.SetToleranceMS(opts.ClockTolerMS)

	idx := &Index{
		Version:   Version,
		Timescale: opts.Timescale,
		Complete:  true,
		Entries:   make([]Entry, 0, 256),
	}
	inOrder := true
	var lastTicks int64
	var highWater int64
	var offset int64
	var prevSeq uint32
	var haveSeq bool
	var scanErr error
	for {
		f, err := fr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			scanErr = err
			idx.Complete = false
			idx.Issues = append(idx.Issues, issueFromError(err))
			break
		}
		p := clk.Observe(f.RTPTimestamp)
		// A backward timestamp is only a clock regression when the sequence
		// also moved forward. A lower sequence is ordinary reordering and is
		// resolved by sorting, not by cutting the timeline.
		seqForward := !haveSeq || int32(f.Seq-prevSeq) > 0
		regressed := p.Regressed && seqForward
		rawTicks := p.Ticks + offset
		ticks := rawTicks
		if regressed {
			// Re-anchor the regression to the high-water mark so the timeline
			// keeps advancing instead of interleaving two clock eras, while the
			// Regressed flag drives an explicit segment split.
			offset += highWater - rawTicks
			ticks = highWater
		}
		if ticks > highWater {
			highWater = ticks
		}
		f.Ticks = ticks
		e := Entry{
			Offset:        f.Offset,
			Length:        f.Len(),
			Seq:           f.Seq,
			RTPTimestamp:  f.RTPTimestamp,
			Ticks:         ticks,
			CaptureMS:     f.CaptureTimeMS,
			Width:         f.Width,
			Height:        f.Height,
			DurationTicks: f.DurationTicks,
			StreamID:      f.StreamID,
			MetaStatus:    string(f.MetaStatus),
			VolumeDBFS:    f.Meta.VolumeDBFS,
			Digest:        hex.EncodeToString(f.Digest[:]),
			Wrapped:       p.Wrapped,
			Regressed:     regressed,
		}
		if regressed {
			idx.Issues = append(idx.Issues, Issue{
				Category:    media.CatTimestampBack,
				Offset:      f.Offset,
				Seq:         f.Seq,
				Message:     fmt.Sprintf("timestamp moved backwards by %d ticks; re-anchored", highWater-rawTicks),
				Recoverable: true,
			})
		}
		prevSeq = f.Seq
		haveSeq = true
		if len(idx.Entries) > 0 && e.Ticks < lastTicks {
			inOrder = false
		}
		lastTicks = e.Ticks
		idx.Entries = append(idx.Entries, e)
	}
	// A failed scan stops at the damaged record and leaves bytes unread. Drain
	// the remainder so the input digest still covers the whole stream and a
	// later repair is detected as a change.
	if !idx.Complete {
		_, _ = fr.Drain()
	}
	idx.InputDigest = hex.EncodeToString(hasher.Sum(nil))
	if !inOrder {
		sort.SliceStable(idx.Entries, func(i, j int) bool {
			if idx.Entries[i].Ticks != idx.Entries[j].Ticks {
				return idx.Entries[i].Ticks < idx.Entries[j].Ticks
			}
			if idx.Entries[i].Seq != idx.Entries[j].Seq {
				return idx.Entries[i].Seq < idx.Entries[j].Seq
			}
			return idx.Entries[i].Offset < idx.Entries[j].Offset
		})
		idx.Reordered = true
	}
	// Media time is reported relative to the earliest sorted frame so every
	// derived range is non-negative even if reordering moved a slightly earlier
	// frame into the first position.
	if len(idx.Entries) > 0 && idx.Entries[0].Ticks != 0 {
		shift := idx.Entries[0].Ticks
		for i := range idx.Entries {
			idx.Entries[i].Ticks -= shift
		}
	}
	idx.EntryCount = len(idx.Entries)
	if len(idx.Entries) > 0 {
		idx.StartTicks = idx.Entries[0].Ticks
		idx.EndTicks = idx.Entries[len(idx.Entries)-1].Ticks
	}
	idx.IndexDigest = ComputeDigest(idx)
	if scanErr != nil {
		return idx, scanErr
	}
	return idx, nil
}

func issueFromError(err error) Issue {
	if e, ok := media.AsError(err); ok {
		return Issue{
			Category:    e.Category,
			Offset:      e.FrameOffset,
			Seq:         e.Seq,
			Message:     e.Message,
			Recoverable: e.Recoverable,
		}
	}
	return Issue{Category: media.CatUnknown, Message: err.Error()}
}

// ComputeDigest derives the index digest from the version, completeness,
// timescale and every entry field. Field order is fixed, so the digest is
// reproducible across runs and platforms.
func ComputeDigest(idx *Index) string {
	h := sha256.New()
	var buf [8]byte
	putU64 := func(v uint64) {
		binary.BigEndian.PutUint64(buf[:], v)
		h.Write(buf[:])
	}
	putU64(uint64(idx.Version))
	putU64(uint64(idx.Timescale))
	if idx.Complete {
		putU64(1)
	} else {
		putU64(0)
	}
	putU64(uint64(len(idx.Entries)))
	for _, e := range idx.Entries {
		putU64(uint64(e.Offset))
		putU64(uint64(e.Length))
		putU64(uint64(e.Seq))
		putU64(uint64(e.RTPTimestamp))
		putU64(uint64(e.Ticks))
		putU64(uint64(e.CaptureMS))
		putU64(uint64(e.Width)<<16 | uint64(e.Height))
		putU64(uint64(e.DurationTicks))
		putU64(uint64(e.StreamID))
		h.Write([]byte(e.MetaStatus))
		h.Write([]byte{0})
		if e.VolumeDBFS != nil {
			var vb [8]byte
			binary.BigEndian.PutUint64(vb[:], math.Float64bits(*e.VolumeDBFS))
			h.Write(vb[:])
		} else {
			h.Write([]byte{0})
		}
		h.Write([]byte(e.Digest))
	}
	for _, is := range idx.Issues {
		h.Write([]byte(is.Category))
		putU64(uint64(is.Offset))
		putU64(uint64(is.Seq))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Marshal renders the index as indented JSON with a trailing newline.
func (idx *Index) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, media.Wrap(media.CatIO, media.StageIndex, err, "marshal index")
	}
	return append(b, '\n'), nil
}

// UnmarshalIndex parses and structurally validates an index document.
func UnmarshalIndex(b []byte) (*Index, error) {
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, media.Wrap(media.CatIndexTruncated, media.StageIndex, err, "parse index")
	}
	if idx.Version != Version {
		return &idx, media.New(media.CatIndexVersion, media.StageIndex,
			"index version %d != current %d", idx.Version, Version)
	}
	if got := ComputeDigest(&idx); got != idx.IndexDigest {
		return &idx, media.New(media.CatIndexTruncated, media.StageIndex,
			"index digest %s does not match entries (computed %s)", idx.IndexDigest, got)
	}
	if idx.EntryCount != len(idx.Entries) {
		return &idx, media.New(media.CatIndexTruncated, media.StageIndex,
			"entry count %d != len(entries) %d", idx.EntryCount, len(idx.Entries))
	}
	return &idx, nil
}

// Resolve looks up the media time for a byte offset, which lets a manifest or
// verifier map a located failure back to its position in the timeline.
func (idx *Index) Resolve(offset int64) (Entry, bool) {
	i := sort.Search(len(idx.Entries), func(i int) bool { return idx.Entries[i].Offset >= offset })
	if i < len(idx.Entries) && idx.Entries[i].Offset == offset {
		return idx.Entries[i], true
	}
	return Entry{}, false
}

// Summary is a compact human-readable index overview.
func (idx *Index) Summary() string {
	return fmt.Sprintf("entries=%d complete=%t reordered=%t digest=%s",
		idx.EntryCount, idx.Complete, idx.Reordered, shortHash(idx.IndexDigest))
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func roundDiv(n, d int64) int64 {
	if d == 0 {
		return 0
	}
	if n >= 0 {
		return (n + d/2) / d
	}
	return -((-n + d/2) / d)
}
