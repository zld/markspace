// Package report builds deterministic per-segment digests and the ratios the
// acceptance run reports on the terminal.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"

	"mediaaudit/internal/model"
	"mediaaudit/internal/render"
	"mediaaudit/internal/timeline"
)

// Canonical encoding separators. They are control characters so payload text
// cannot be confused with a field boundary.
const sep = 0x1f

// writeField writes a length-prefixed field so arbitrary payload bytes cannot
// forge a separator.
func writeField(w io.Writer, s string) {
	io.WriteString(w, strconv.Itoa(len(s)))
	io.WriteString(w, ":")
	io.WriteString(w, s)
	w.Write([]byte{sep})
}

// CanonicalItem appends the deterministic encoding of one timeline item.
func CanonicalItem(w io.Writer, it timeline.Item) {
	writeField(w, strconv.Itoa(it.Seq))
	writeField(w, string(it.Kind))
	writeField(w, it.Track.ID)
	writeField(w, string(it.Track.Kind))
	writeField(w, it.Key)
	writeField(w, it.Span.Start.String())
	writeField(w, it.Span.End.String())
	writeField(w, it.Source)
	writeField(w, strconv.Itoa(it.SourceIdx))
	writeField(w, it.Payload)
	w.Write([]byte{'\n'})
}

// CanonicalFrame appends the deterministic encoding of one frame record.
func CanonicalFrame(w io.Writer, fr render.FrameRecord) {
	writeField(w, fr.Point.String())
	writeField(w, strconv.Itoa(fr.SampleIdx))
	writeField(w, strconv.FormatBool(fr.Keyframe))
	writeField(w, fr.Origin)
	writeField(w, fr.PixelHash)
	writeField(w, fr.ByteHash)
	writeField(w, strconv.Itoa(fr.Width))
	writeField(w, strconv.Itoa(fr.Height))
	writeField(w, strconv.Itoa(int(fr.PayloadLen)))
	if fr.Error != nil {
		writeField(w, string(fr.Error.Code))
		writeField(w, fr.Error.Pos.String())
		writeField(w, fr.Error.Message)
	} else {
		writeField(w, "")
		writeField(w, "")
		writeField(w, "")
	}
	w.Write([]byte{'\n'})
}

// Hash writes the hash of a byte stream as lowercase hex.
func Hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ItemsDigest hashes an item slice in sequence order.
func ItemsDigest(items []timeline.Item) string {
	h := sha256.New()
	for _, it := range items {
		CanonicalItem(h, it)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FramesDigest hashes frame records in point order.
func FramesDigest(frames []render.FrameRecord) string {
	h := sha256.New()
	for _, fr := range frames {
		CanonicalFrame(h, fr)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// InputDigest identifies the exact inputs and parameters of a chunk. A resume
// recomputes it and refuses to continue when it differs from the committed one.
func InputDigest(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		writeField(h, p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Chunk is the auditable unit of work: a contiguous slice of the timeline.
type Chunk struct {
	Index        int                  `json:"index"`
	FirstSeq     int                  `json:"first_seq"`
	LastSeq      int                  `json:"last_seq"`
	InputDigest  string               `json:"input_digest"`
	ItemsDigest  string               `json:"items_digest"`
	FramesDigest string               `json:"frames_digest"`
	Digest       string               `json:"digest"`
	Items        []timeline.Item      `json:"-"`
	Frames       []render.FrameRecord `json:"-"`
	Errors       model.Errors         `json:"-"`
}

// Segment returns a deterministic name for the chunk.
func (c Chunk) Segment() string { return fmt.Sprintf("chunk-%05d", c.Index) }

// Finalize computes the digests for the chunk from its contents.
func (c *Chunk) Finalize() {
	c.ItemsDigest = ItemsDigest(c.Items)
	c.FramesDigest = FramesDigest(c.Frames)
	c.Digest = InputDigest(c.ItemsDigest, c.FramesDigest, c.InputDigest)
	if len(c.Items) > 0 {
		c.FirstSeq = c.Items[0].Seq
		c.LastSeq = c.Items[len(c.Items)-1].Seq
	}
}

// Split partitions items into fixed-size chunks. The last chunk may be short.
// An empty timeline produces no chunks.
func Split(items []timeline.Item, size int) [][]timeline.Item {
	if size <= 0 || len(items) == 0 {
		return nil
	}
	var out [][]timeline.Item
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		out = append(out, items[start:end])
	}
	return out
}

// Ratio is an exact count pair rendered to three decimal places.
type Ratio struct {
	Num, Den int
}

// NewRatio clamps nonsensical inputs to zero.
func NewRatio(num, den int) Ratio {
	if den <= 0 {
		return Ratio{}
	}
	return Ratio{Num: num, Den: den}
}

// Value is for sorting/logging only; equality checks must use the counts.
func (r Ratio) Value() float64 {
	if r.Den == 0 {
		return 0
	}
	return float64(r.Num) / float64(r.Den)
}

// String renders the value with three decimals, matching the acceptance log.
func (r Ratio) String() string { return fmt.Sprintf("%.3f", r.Value()) }

// Metrics are the acceptance ratios.
type Metrics struct {
	RoundtripConsistency Ratio `json:"roundtrip_consistency"`
	KeyframeIndexHitRate Ratio `json:"keyframe_index_hit_rate"`
	SegmentHashMatch     Ratio `json:"segment_hash_match"`
}

// AllHeld reports whether every ratio is a perfect score.
func (m Metrics) AllHeld() bool {
	return m.RoundtripConsistency.Num == m.RoundtripConsistency.Den &&
		m.KeyframeIndexHitRate.Num == m.KeyframeIndexHitRate.Den &&
		m.SegmentHashMatch.Num == m.SegmentHashMatch.Den
}
