// Package meta normalizes timed metadata families (subtitles, chapters, ID3,
// emsg and SEI) onto one canonical record with explicit track ownership,
// timescale and payload digest. Parsing is strict and never repairs a record
// silently.
package meta

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Kind identifies the metadata family of a record.
type Kind uint8

const (
	// KindSubtitle is a WebVTT-style subtitle cue.
	KindSubtitle Kind = 1
	// KindChapter is a chapter marker.
	KindChapter Kind = 2
	// KindID3 is an ID3v2.4 tag.
	KindID3 Kind = 3
	// KindEmsg is an ISO emsg event.
	KindEmsg Kind = 4
	// KindSEI is an H.264 SEI user_data_unregistered message.
	KindSEI Kind = 5
)

func (k Kind) String() string {
	switch k {
	case KindSubtitle:
		return "subtitle"
	case KindChapter:
		return "chapter"
	case KindID3:
		return "id3"
	case KindEmsg:
		return "emsg"
	case KindSEI:
		return "sei"
	default:
		return fmt.Sprintf("kind(%d)", uint8(k))
	}
}

// Limits bound every allocation performed by this package.
const (
	MaxRecordLabel   = 256
	MaxRecordPayload = 8192
	MaxTextLen       = 1024
	MaxID3TagBytes   = 64 * 1024
	MaxID3Frames     = 64
	// SEIUUID is the 16-byte user_data_unregistered identifier used by the
	// platform. It is always present verbatim as the first 16 payload bytes.
	SEIUUID = "MDPIPEMETA000001"
)

var (
	// ErrMetaFormat reports a structurally invalid metadata record.
	ErrMetaFormat = errors.New("meta: malformed record")
	// ErrMetaLimit reports an allocation beyond the package limits.
	ErrMetaLimit = errors.New("meta: size limit exceeded")
	// ErrMetaDigest reports a payload digest mismatch.
	ErrMetaDigest = errors.New("meta: payload digest mismatch")
)

// Record is one normalized timed metadata item. TimeTicks is in Timescale
// units. Digest always covers Payload exactly as stored.
type Record struct {
	Kind         Kind
	TrackID      uint32
	Timescale    uint32
	TimeTicks    uint64
	DurationTick uint64
	Label        string
	Payload      []byte
	Digest       [32]byte
}

// NewRecord validates inputs and computes the payload digest.
func NewRecord(kind Kind, trackID, timescale uint32, ticks, duration uint64, label string, payload []byte) (Record, error) {
	r := Record{
		Kind:         kind,
		TrackID:      trackID,
		Timescale:    timescale,
		TimeTicks:    ticks,
		DurationTick: duration,
		Label:        label,
		Payload:      append([]byte(nil), payload...),
	}
	if err := r.validate(); err != nil {
		return Record{}, err
	}
	r.Digest = sha256.Sum256(r.Payload)
	return r, nil
}

func (r Record) validate() error {
	if r.Kind < KindSubtitle || r.Kind > KindSEI {
		return fmt.Errorf("%w: kind %d", ErrMetaFormat, r.Kind)
	}
	if r.TrackID == 0 {
		return fmt.Errorf("%w: track id 0", ErrMetaFormat)
	}
	if r.Timescale == 0 || r.Timescale > 1_000_000 {
		return fmt.Errorf("%w: timescale %d", ErrMetaFormat, r.Timescale)
	}
	if len(r.Label) > MaxRecordLabel {
		return fmt.Errorf("%w: label %d bytes", ErrMetaLimit, len(r.Label))
	}
	if len(r.Payload) > MaxRecordPayload {
		return fmt.Errorf("%w: payload %d bytes", ErrMetaLimit, len(r.Payload))
	}
	return nil
}

// WithDigest returns a copy of r with a recomputed digest.
func (r Record) WithDigest() Record {
	r.Digest = sha256.Sum256(r.Payload)
	return r
}

// CheckDigest verifies the recorded digest against the payload.
func (r Record) CheckDigest() error {
	if sha256.Sum256(r.Payload) != r.Digest {
		return fmt.Errorf("%w: %s track %d", ErrMetaDigest, r.Kind, r.TrackID)
	}
	return nil
}

const canonicalMagic = "MDR1"

// Canonical encodes the record deterministically. It is the serialization used
// for cache keys and for round-trip checks.
func (r Record) Canonical() []byte {
	buf := make([]byte, 0, 4+1+4+4+8+8+2+len(r.Label)+4+len(r.Payload)+32)
	buf = append(buf, canonicalMagic...)
	buf = append(buf, byte(r.Kind))
	buf = binary.BigEndian.AppendUint32(buf, r.TrackID)
	buf = binary.BigEndian.AppendUint32(buf, r.Timescale)
	buf = binary.BigEndian.AppendUint64(buf, r.TimeTicks)
	buf = binary.BigEndian.AppendUint64(buf, r.DurationTick)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(r.Label)))
	buf = append(buf, r.Label...)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(r.Payload)))
	buf = append(buf, r.Payload...)
	buf = append(buf, r.Digest[:]...)
	return buf
}

// ParseCanonical decodes a record produced by Canonical and re-verifies every
// field including the payload digest.
func ParseCanonical(b []byte) (Record, error) {
	var r Record
	if len(b) < 4+1+4+4+8+8+2+4+32 || string(b[:4]) != canonicalMagic {
		return r, fmt.Errorf("%w: bad canonical header", ErrMetaFormat)
	}
	buf := bytes.NewReader(b[4:])
	read := func(n int) ([]byte, error) {
		if buf.Len() < n {
			return nil, fmt.Errorf("%w: truncated canonical record", ErrMetaFormat)
		}
		out := make([]byte, n)
		_, _ = buf.Read(out)
		return out, nil
	}
	hdr, _ := read(1)
	r.Kind = Kind(hdr[0])
	v, _ := read(4)
	r.TrackID = binary.BigEndian.Uint32(v)
	v, _ = read(4)
	r.Timescale = binary.BigEndian.Uint32(v)
	v, _ = read(8)
	r.TimeTicks = binary.BigEndian.Uint64(v)
	v, _ = read(8)
	r.DurationTick = binary.BigEndian.Uint64(v)
	v, _ = read(2)
	labelLen := int(binary.BigEndian.Uint16(v))
	if labelLen > MaxRecordLabel {
		return r, fmt.Errorf("%w: label %d", ErrMetaLimit, labelLen)
	}
	lbl, err := read(labelLen)
	if err != nil {
		return r, err
	}
	r.Label = string(lbl)
	v, err = read(4)
	if err != nil {
		return r, err
	}
	payloadLen := int(binary.BigEndian.Uint32(v))
	if payloadLen > MaxRecordPayload {
		return r, fmt.Errorf("%w: payload %d", ErrMetaLimit, payloadLen)
	}
	payload, err := read(payloadLen)
	if err != nil {
		return r, err
	}
	r.Payload = payload
	digest, err := read(32)
	if err != nil {
		return r, err
	}
	copy(r.Digest[:], digest)
	if buf.Len() != 0 {
		return r, fmt.Errorf("%w: %d trailing bytes", ErrMetaFormat, buf.Len())
	}
	if err := r.validate(); err != nil {
		return r, err
	}
	if err := r.CheckDigest(); err != nil {
		return r, err
	}
	return r, nil
}

// Cue is a normalized subtitle cue. Times are milliseconds.
type Cue struct {
	Seq     uint32
	StartMS uint64
	EndMS   uint64
	Text    string
}

// EncodeCue renders the cue as a WebVTT cue block.
func EncodeCue(c Cue) ([]byte, error) {
	if c.EndMS <= c.StartMS {
		return nil, fmt.Errorf("%w: cue end %d <= start %d", ErrMetaFormat, c.EndMS, c.StartMS)
	}
	if len(c.Text) == 0 || len(c.Text) > MaxTextLen || strings.ContainsAny(c.Text, "\r\n") {
		return nil, fmt.Errorf("%w: cue text invalid", ErrMetaFormat)
	}
	s := fmt.Sprintf("WEBVTT\n\n%d\n%s --> %s\n%s\n", c.Seq, formatMS(c.StartMS), formatMS(c.EndMS), c.Text)
	return []byte(s), nil
}

// ParseCue accepts a cue block produced by EncodeCue.
func ParseCue(b []byte) (Cue, error) {
	var c Cue
	if len(b) > MaxRecordPayload {
		return c, fmt.Errorf("%w: cue %d bytes", ErrMetaLimit, len(b))
	}
	text := string(b)
	if !strings.HasPrefix(text, "WEBVTT\n\n") {
		return c, fmt.Errorf("%w: missing WEBVTT header", ErrMetaFormat)
	}
	lines := strings.Split(strings.TrimSuffix(text[len("WEBVTT\n\n"):], "\n"), "\n")
	if len(lines) != 3 {
		return c, fmt.Errorf("%w: cue has %d lines", ErrMetaFormat, len(lines))
	}
	seq, err := strconv.ParseUint(strings.TrimSpace(lines[0]), 10, 32)
	if err != nil {
		return c, fmt.Errorf("%w: cue sequence: %v", ErrMetaFormat, err)
	}
	c.Seq = uint32(seq)
	parts := strings.Split(lines[1], " --> ")
	if len(parts) != 2 {
		return c, fmt.Errorf("%w: cue timing", ErrMetaFormat)
	}
	if c.StartMS, err = parseMS(parts[0]); err != nil {
		return c, err
	}
	if c.EndMS, err = parseMS(parts[1]); err != nil {
		return c, err
	}
	if c.EndMS <= c.StartMS {
		return c, fmt.Errorf("%w: cue end %d <= start %d", ErrMetaFormat, c.EndMS, c.StartMS)
	}
	if lines[2] == "" || len(lines[2]) > MaxTextLen {
		return c, fmt.Errorf("%w: cue text invalid", ErrMetaFormat)
	}
	c.Text = lines[2]
	return c, nil
}

func formatMS(ms uint64) string {
	h := ms / 3600000
	m := (ms / 60000) % 60
	s := (ms / 1000) % 60
	f := ms % 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, f)
}

func parseMS(s string) (uint64, error) {
	var h, m, sec, frac uint64
	n, err := fmt.Sscanf(strings.TrimSpace(s), "%02d:%02d:%02d.%03d", &h, &m, &sec, &frac)
	if err != nil || n != 4 || m > 59 || sec > 59 || frac > 999 {
		return 0, fmt.Errorf("%w: time %q", ErrMetaFormat, s)
	}
	return h*3600000 + m*60000 + sec*1000 + frac, nil
}

// NewSubtitleRecord builds a cue record owned by trackID at ms resolution.
func NewSubtitleRecord(trackID uint32, c Cue) (Record, error) {
	payload, err := EncodeCue(c)
	if err != nil {
		return Record{}, err
	}
	return NewRecord(KindSubtitle, trackID, 1000, c.StartMS, c.EndMS-c.StartMS, "webvtt", payload)
}

// NewChapterRecord builds a chapter marker record in ms.
func NewChapterRecord(trackID uint32, index uint32, startMS, endMS uint64, title string) (Record, error) {
	if endMS <= startMS || len(title) == 0 || len(title) > MaxTextLen || strings.ContainsAny(title, "\r\n|") {
		return Record{}, fmt.Errorf("%w: chapter invalid", ErrMetaFormat)
	}
	payload := []byte(fmt.Sprintf("%d|%d|%d|%s", index, startMS, endMS, title))
	return NewRecord(KindChapter, trackID, 1000, startMS, endMS-startMS, "chapter", payload)
}

// Chapter is a parsed chapter payload.
type Chapter struct {
	Index   uint32
	StartMS uint64
	EndMS   uint64
	Title   string
}

// ParseChapter decodes a chapter payload.
func ParseChapter(b []byte) (Chapter, error) {
	var c Chapter
	parts := strings.Split(string(b), "|")
	if len(parts) != 4 || len(parts[3]) == 0 || len(parts[3]) > MaxTextLen {
		return c, fmt.Errorf("%w: chapter payload", ErrMetaFormat)
	}
	v, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return c, fmt.Errorf("%w: chapter index", ErrMetaFormat)
	}
	c.Index = uint32(v)
	if c.StartMS, err = strconv.ParseUint(parts[1], 10, 64); err != nil {
		return c, fmt.Errorf("%w: chapter start", ErrMetaFormat)
	}
	if c.EndMS, err = strconv.ParseUint(parts[2], 10, 64); err != nil {
		return c, fmt.Errorf("%w: chapter end", ErrMetaFormat)
	}
	c.Title = parts[3]
	if c.EndMS <= c.StartMS {
		return c, fmt.Errorf("%w: chapter end <= start", ErrMetaFormat)
	}
	return c, nil
}

// schemeLabel maps an emsg scheme URI to a short, bounded record label.
func schemeLabel(scheme string) string {
	const prefix = "urn:mediapipe:"
	if strings.HasPrefix(scheme, prefix) && len(scheme) <= MaxRecordLabel {
		return scheme[len(prefix):]
	}
	return scheme
}
