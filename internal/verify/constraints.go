// Package verify independently re-reads a generated artifact and checks it
// against the run's constraints. It shares no state with the generator: every
// fact it asserts is derived from the bytes on disk plus the manifest.
package verify

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"

	"rtpregress/internal/media"
)

// Artifact layout, version 1. All integers are big-endian. The constants are
// deliberately duplicated in the writer and the verifier: a format drift
// between the two must fail loudly rather than be papered over by a shared
// definition. The layout is sized so a small run fits a 4096-byte cap.
//
//	header (64 bytes)
//	  0  magic "RTRG"
//	  4  version uint16
//	  6  flags uint16
//	  8  segment_ms uint32
//	 12  precision_us uint32
//	 16  event_count uint32
//	 20  segment_count uint32
//	 24  text_bytes uint32
//	 28  record_bytes uint32
//	 32  body_sha256 [32]byte
//	segment table: segment_count * { start_ns int64, event_count uint32,
//	                                  reserved uint32 }
//	record body: event_count * { time_ns int64, segment uint32,
//	                              text_len uint16, flags uint16,
//	                              track byte, source byte, reserved uint16,
//	                              text [text_len]byte }
const (
	Magic          = "RTRG"
	LayoutVersion  = 1
	HeaderSize     = 64
	SegEntrySize   = 16
	EventFixedSize = 20
	DigestOffset   = 32
)

// Segment is one entry of the artifact's segment table.
type Segment struct {
	StartNs    int64 `json:"startNs"`
	EventCount int   `json:"eventCount"`
}

// Record is one decoded event record.
type Record struct {
	TimeNs  int64  `json:"timeNs"`
	Segment int    `json:"segment"`
	Track   string `json:"track"`
	Source  string `json:"source"`
	Text    string `json:"text"`
	// Offset is the byte position of the record inside the artifact, used
	// to locate the first mismatch precisely.
	Offset int64 `json:"offset"`
}

// Artifact is a fully decoded output file.
type Artifact struct {
	ParamSegmentMS   int
	ParamPrecisionUS int
	Segments         []Segment
	Records          []Record
	// TextBytes is the total length of all record texts.
	TextBytes int
	// BodySHA256 is the digest the writer embedded over the record body.
	BodySHA256 string
	// FileSHA256 is the digest of the whole file as read from disk.
	FileSHA256 string
	// Size is the file's byte length.
	Size int
}

// Expected is what the run says the artifact must contain.
type Expected struct {
	Events         int
	MaxDeviationNs int64
	MaxBytes       int
	QualityMin     float64
	Quality        float64
	SegmentMS      int
	PrecisionUS    int
	Segments       int
}

// Result reports the verifier's verdict.
type Result struct {
	OK      bool            `json:"ok"`
	Summary ArtifactSummary `json:"summary"`
	// Mismatch localizes the first failed check.
	Mismatch *Mismatch `json:"mismatch,omitempty"`
}

// ArtifactSummary is the stable digest block printed for every run.
type ArtifactSummary struct {
	Events         int            `json:"events"`
	BySource       map[string]int `json:"bySource"`
	ByTrack        map[string]int `json:"byTrack"`
	Segments       int            `json:"segments"`
	MaxDeviationMs float64        `json:"maxDeviationMs"`
	FileBytes      int            `json:"fileBytes"`
	FileSHA256     string         `json:"fileSha256"`
	BodySHA256     string         `json:"bodySha256"`
}

// Mismatch identifies a failed constraint down to the offending record.
type Mismatch struct {
	Check     string `json:"check"`
	Track     string `json:"track,omitempty"`
	Source    string `json:"source,omitempty"`
	Segment   int    `json:"segment,omitempty"`
	Offset    int64  `json:"offset"`
	HasOffset bool   `json:"hasOffset"`
	Detail    string `json:"detail"`
}

func (m *Mismatch) Error() string {
	s := m.Check + ": " + m.Detail
	if m.Track != "" {
		s += fmt.Sprintf(" (track=%s", m.Track)
		if m.Source != "" {
			s += " source=" + m.Source
		}
		if m.HasOffset {
			s += fmt.Sprintf(" offset=%d", m.Offset)
		}
		s += ")"
	} else if m.HasOffset {
		s += fmt.Sprintf(" (offset=%d)", m.Offset)
	}
	return s
}

// Read decodes an artifact file from disk.
func Read(path string) (*Artifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, media.At(media.Newf(media.KindVerify, "read artifact %s: %v", path, err),
			media.Loc{Stage: "verify"})
	}
	return Decode(data)
}

// Decode parses artifact bytes and validates every structural invariant.
func Decode(data []byte) (*Artifact, error) {
	fail := func(check, detail string, off int64, has bool) error {
		return media.At(media.New(media.KindVerify, check+": "+detail),
			media.Loc{Stage: "verify", Offset: off, HasOffset: has})
	}
	if len(data) < HeaderSize {
		return nil, fail("header", fmt.Sprintf("artifact is %d bytes, shorter than the %d-byte header", len(data), HeaderSize), 0, false)
	}
	if string(data[0:4]) != Magic {
		return nil, fail("header", fmt.Sprintf("bad magic %q", data[0:4]), 0, true)
	}
	if v := binary.BigEndian.Uint16(data[4:6]); v != LayoutVersion {
		return nil, fail("header", fmt.Sprintf("unsupported layout version %d", v), 4, true)
	}
	a := &Artifact{
		ParamSegmentMS:   int(binary.BigEndian.Uint32(data[8:12])),
		ParamPrecisionUS: int(binary.BigEndian.Uint32(data[12:16])),
	}
	eventCount := int(binary.BigEndian.Uint32(data[16:20]))
	segCount := int(binary.BigEndian.Uint32(data[20:24]))
	a.TextBytes = int(binary.BigEndian.Uint32(data[24:28]))
	recordBytes := int(binary.BigEndian.Uint32(data[28:32]))
	a.BodySHA256 = "sha256:" + hex.EncodeToString(data[DigestOffset:DigestOffset+32])
	if segCount < 0 || eventCount < 0 || recordBytes < 0 {
		return nil, fail("header", "negative section size", 16, true)
	}

	segStart := HeaderSize
	segEnd := segStart + segCount*SegEntrySize
	if segEnd > len(data) {
		return nil, fail("segment table", fmt.Sprintf("needs %d bytes, file has %d", segEnd, len(data)), int64(segStart), true)
	}
	a.Segments = make([]Segment, segCount)
	for i := 0; i < segCount; i++ {
		off := segStart + i*SegEntrySize
		seg := Segment{
			StartNs:    int64(binary.BigEndian.Uint64(data[off : off+8])),
			EventCount: int(binary.BigEndian.Uint32(data[off+8 : off+12])),
		}
		if i > 0 && seg.StartNs <= a.Segments[i-1].StartNs {
			return nil, fail("segment table", "segment starts are not strictly increasing", int64(off), true)
		}
		if seg.EventCount < 0 {
			return nil, fail("segment table", "negative event count", int64(off+8), true)
		}
		a.Segments[i] = seg
	}

	bodyStart := segEnd
	bodyEnd := bodyStart + recordBytes
	if bodyEnd > len(data) {
		return nil, fail("record body", fmt.Sprintf("needs %d bytes, file has %d", bodyEnd, len(data)), int64(bodyStart), true)
	}
	if bodyEnd != len(data) {
		return nil, fail("record body", fmt.Sprintf("%d trailing bytes after the body", len(data)-bodyEnd), int64(bodyEnd), true)
	}
	body := data[bodyStart:bodyEnd]
	sum := sha256.Sum256(body)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != a.BodySHA256 {
		return nil, fail("body digest", fmt.Sprintf("embedded %s, recomputed %s", a.BodySHA256, got), int64(bodyStart), true)
	}

	// Walk event records.
	pos := 0
	a.Records = make([]Record, 0, eventCount)
	textSum := 0
	for i := 0; i < eventCount; i++ {
		if pos+EventFixedSize > len(body) {
			return nil, fail("record body", fmt.Sprintf("record %d header truncated", i+1), int64(bodyStart+pos), true)
		}
		rec := Record{
			TimeNs:  int64(binary.BigEndian.Uint64(body[pos : pos+8])),
			Segment: int(binary.BigEndian.Uint32(body[pos+8 : pos+12])),
			Track:   trackName(body[pos+16]),
			Source:  sourceName(body[pos+17]),
			Offset:  int64(bodyStart + pos),
		}
		textLen := int(binary.BigEndian.Uint16(body[pos+12 : pos+14]))
		if pos+EventFixedSize+textLen > len(body) {
			return nil, fail("record body", fmt.Sprintf("record %d text length %d overruns the body", i+1, textLen), int64(bodyStart+pos), true)
		}
		rec.Text = string(body[pos+EventFixedSize : pos+EventFixedSize+textLen])
		textSum += textLen
		pos += EventFixedSize + textLen
		a.Records = append(a.Records, rec)
	}
	if pos != len(body) {
		return nil, fail("record body", fmt.Sprintf("%d unconsumed bytes", len(body)-pos), int64(bodyStart+pos), true)
	}
	if textSum != a.TextBytes {
		return nil, fail("header", fmt.Sprintf("declared text bytes %d, actual %d", a.TextBytes, textSum), 28, true)
	}

	fileSum := sha256.Sum256(data)
	a.FileSHA256 = "sha256:" + hex.EncodeToString(fileSum[:])
	a.Size = len(data)
	return a, nil
}

// Check runs every constraint against the decoded artifact. It returns the
// first mismatch, located to the responsible record, or an OK result.
func Check(a *Artifact, exp Expected) (*Result, error) {
	summary := a.Summary()
	mismatch := func(check string, rec *Record, detail string) *Result {
		m := &Mismatch{Check: check, Detail: detail}
		if rec != nil {
			m.Track, m.Source, m.Segment = rec.Track, rec.Source, rec.Segment
			m.Offset, m.HasOffset = rec.Offset, true
		}
		return &Result{OK: false, Summary: summary, Mismatch: m}
	}

	// Event count.
	if len(a.Records) != exp.Events {
		detail := fmt.Sprintf("artifact has %d events, expected %d", len(a.Records), exp.Events)
		var first *Record
		if len(a.Records) > 0 && exp.Events < len(a.Records) {
			// The first extra record is where the divergence begins.
			first = &a.Records[0]
		}
		return mismatch("event-count", first, detail), nil
	}

	// Segment table must agree with the per-record segment fields.
	segCounts := make([]int, len(a.Segments))
	firstBadSeg := (*Record)(nil)
	for i := range a.Records {
		r := &a.Records[i]
		if r.Segment < 0 || r.Segment >= len(a.Segments) {
			return mismatch("segment-index", r,
				fmt.Sprintf("event %d names segment %d of %d", i+1, r.Segment, len(a.Segments))), nil
		}
		segCounts[r.Segment]++
		if r.Segment > 0 && r.TimeNs < a.Segments[r.Segment].StartNs {
			firstBadSeg = r
		}
	}
	if firstBadSeg != nil {
		return mismatch("segment-boundary", firstBadSeg,
			"event precedes its segment start"), nil
	}
	for i, seg := range a.Segments {
		if segCounts[i] != seg.EventCount {
			return mismatch("segment-count", nil,
				fmt.Sprintf("segment %d declares %d events, holds %d", i, seg.EventCount, segCounts[i])), nil
		}
	}

	// Time accuracy: every record must lie within the declared deviation of
	// the precision grid.
	step := int64(exp.PrecisionUS) * 1000
	if step <= 0 {
		return mismatch("precision", nil, fmt.Sprintf("invalid precision %dus", exp.PrecisionUS)), nil
	}
	var maxDev int64
	for i := range a.Records {
		r := &a.Records[i]
		dev := r.TimeNs % step
		if dev < 0 {
			dev = -dev
		}
		if dev > exp.MaxDeviationNs {
			return mismatch("deviation", r,
				fmt.Sprintf("event %d is %dns off the %dns grid, limit %dns",
					i+1, dev, step, exp.MaxDeviationNs)), nil
		}
		if dev > maxDev {
			maxDev = dev
		}
	}
	summary.MaxDeviationMs = float64(maxDev) / 1e6

	// Byte ceiling.
	if a.Size > exp.MaxBytes {
		return mismatch("max-bytes", nil,
			fmt.Sprintf("artifact is %d bytes, cap %d", a.Size, exp.MaxBytes)), nil
	}

	// Quality floor.
	if exp.Quality < exp.QualityMin {
		return mismatch("quality-floor", nil,
			fmt.Sprintf("quality %.3f is below floor %.3f", exp.Quality, exp.QualityMin)), nil
	}

	// Parameter echo: the artifact must describe the plan actually chosen.
	if a.ParamSegmentMS != exp.SegmentMS || a.ParamPrecisionUS != exp.PrecisionUS {
		return mismatch("parameter-echo", nil,
			fmt.Sprintf("artifact says segment=%dms precision=%dus, plan says %dms/%dus",
				a.ParamSegmentMS, a.ParamPrecisionUS, exp.SegmentMS, exp.PrecisionUS)), nil
	}
	if len(a.Segments) != exp.Segments {
		return mismatch("segment-count", nil,
			fmt.Sprintf("artifact has %d segments, plan has %d", len(a.Segments), exp.Segments)), nil
	}
	return &Result{OK: true, Summary: summary}, nil
}

// Summary builds the stable digest block for an artifact.
func (a *Artifact) Summary() ArtifactSummary {
	s := ArtifactSummary{
		Events: len(a.Records), BySource: map[string]int{}, ByTrack: map[string]int{},
		Segments: len(a.Segments), FileBytes: a.Size,
		FileSHA256: a.FileSHA256, BodySHA256: a.BodySHA256,
	}
	for _, r := range a.Records {
		s.BySource[r.Source]++
		s.ByTrack[r.Track]++
	}
	return s
}

// StableJSON marshals v with sorted keys so digests never depend on map
// iteration order.
func StableJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	// Round-trip through a generic value so object keys can be reordered.
	// Numbers become float64 here; callers that need integer forms should
	// marshal those fields as strings.
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return MarshalSorted(generic)
}

// MarshalSorted serialises a decoded JSON value with object keys in sorted
// order, which is what makes cross-run digests reproducible.
func MarshalSorted(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeSorted(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeSorted(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		_, err := buf.WriteString("null")
		return err
	case bool:
		_, err := buf.WriteString(fmt.Sprint(x))
		return err
	case float64:
		_, err := buf.WriteString(trimFloat(x))
		return err
	case string:
		b, err := json.Marshal(x)
		if err != nil {
			return err
		}
		_, err = buf.Write(b)
		return err
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeSorted(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			if err := writeSorted(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return err
		}
		buf.Write(b)
		return nil
	}
}

func trimFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// DigestSummary hashes the stable JSON of a summary block.
func DigestSummary(v any) (string, error) {
	b, err := StableJSON(v)
	if err != nil {
		return "", err
	}
	return SHA256(b), nil
}

// DigestArtifact returns the digest of an artifact file's bytes.
func DigestArtifact(a *Artifact) string { return a.FileSHA256 }
