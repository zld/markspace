package output

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"

	"replayproof/internal/index"
	"replayproof/internal/media"
	"replayproof/internal/repair"
)

// SampleMap is the per-sample input-to-clip record printed by verify. It is
// the unit of the "逐样本" audit: source time, clip time, byte offsets and
// the measured time deviation.
type SampleMap struct {
	TrackID  uint32
	Number   int
	Source   media.Rational
	Duration media.Rational
	// ClipName is empty when the sample falls outside every clip.
	ClipName string
	// ClipTime is the sample time relative to the clip start.
	ClipTime     media.Rational
	SourceOffset int64
	ClipOffset   int64
	Size         uint32
	Keyframe     bool
	// TimeError is the deviation between the source time and the time
	// reconstructed from the published clip, in microseconds.
	TimeError int64
	// ByteError is non-zero when the source and clip payloads differ.
	ByteError int64
}

// Line renders one record in a fixed, greppable format.
func (m SampleMap) Line() string {
	clip := "-"
	clipTime := "-"
	clipOff := "-"
	if m.ClipName != "" {
		clip = m.ClipName
		clipTime = fmt.Sprintf("%d", m.ClipTime.Micros())
		clipOff = fmt.Sprintf("%d", m.ClipOffset)
	}
	key := 0
	if m.Keyframe {
		key = 1
	}
	return fmt.Sprintf(
		"map track=%d n=%02d src_us=%d dur_us=%d clip=%s clip_us=%s src_off=%d clip_off=%s size=%d key=%d time_err_us=%d byte_err=%d",
		m.TrackID, m.Number, m.Source.Micros(), m.Duration.Micros(), clip, clipTime,
		m.SourceOffset, clipOff, m.Size, key, m.TimeError, m.ByteError)
}

// BadCase records a corrupt or conflicted input that was rejected.
type BadCase struct {
	Name   string
	Code   media.Code
	Offset int64
	Track  uint32
	Detail string
	// Published is false when no clip was produced from this input.
	Published bool
}

// Line renders the bad case with its failure position.
func (b BadCase) Line() string {
	off := "unknown"
	if b.Offset >= 0 {
		off = fmt.Sprintf("%d", b.Offset)
	}
	return fmt.Sprintf("bad name=%s code=%s offset=%s track=%d published=%v detail=%q",
		b.Name, b.Code, off, b.Track, b.Published, b.Detail)
}

// MetadataRecord describes how a metadata field was resolved.
type MetadataRecord struct {
	Field      string
	Source     string
	Value      string
	Conflict   bool
	Candidates []string
}

// Summary is the complete, published result of a verify run.
type Summary struct {
	Seed        int64
	MapCount    int
	Clips       []*Clip
	Maps        []SampleMap
	BadCases    []BadCase
	Fixes       []repair.TimestampFix
	Metadata    []MetadataRecord
	QueuePeak   int
	Workers     int
	QueueSize   int
	MaxTimeErr  int64
	ByteErrors  int64
	Resumed     bool
	ResumeEqual bool
	Canceled    bool
	IndexPath   string
	IndexHash   string
}

// ClipByEvent returns the clip for an event index.
func (s *Summary) ClipByEvent(n int) *Clip {
	for _, c := range s.Clips {
		if c.Spec.EventIndex == n {
			return c
		}
	}
	return nil
}

// KeyMetrics renders the single-line acceptance metrics.
func (s *Summary) KeyMetrics() string {
	return fmt.Sprintf(
		"map_count=%d clips=%d max_time_error_us=%d byte_error=%d bad_cases=%d queue_peak=%d resume_digest_equal=%v",
		s.MapCount, len(s.Clips), s.MaxTimeErr, s.ByteErrors, len(s.BadCases), s.QueuePeak, s.ResumeEqual)
}

// ComputeErrors fills MapCount, MaxTimeErr and ByteErrors from the sample map.
func (s *Summary) ComputeErrors() {
	s.MapCount = len(s.Maps)
	s.MaxTimeErr = 0
	s.ByteErrors = 0
	for _, m := range s.Maps {
		if m.TimeError > s.MaxTimeErr {
			s.MaxTimeErr = m.TimeError
		}
		if m.ByteError != 0 {
			s.ByteErrors++
		}
	}
}

// Canonical returns the byte-stable representation hashed into the digest.
// Everything that must be reproducible for the same seed appears here:
// sample maps, clip hashes, metadata choices, repairs and the deterministic
// metrics. Operational counters such as queue_peak and wall-clock timings
// are deliberately excluded so scheduling jitter cannot change the digest.
func (s *Summary) Canonical() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "seed=%d\n", s.Seed)
	fmt.Fprintf(&b, "map_count=%d clips=%d max_time_error_us=%d byte_error=%d bad_cases=%d resume_digest_equal=%v\n",
		s.MapCount, len(s.Clips), s.MaxTimeErr, s.ByteErrors, len(s.BadCases), s.ResumeEqual)
	for _, m := range s.Maps {
		fmt.Fprintf(&b, "%s\n", m.Line())
	}
	clips := append([]*Clip(nil), s.Clips...)
	sort.Slice(clips, func(i, j int) bool { return clips[i].Spec.EventIndex < clips[j].Spec.EventIndex })
	for _, c := range clips {
		fmt.Fprintf(&b, "clip event=%d start_us=%d end_us=%d bytes=%d sha=%s shift_us=%d\n",
			c.Spec.EventIndex, c.Spec.Start.Micros(), c.Spec.End.Micros(), c.Bytes, c.Hash, c.Shift.Micros())
	}
	for _, bc := range s.BadCases {
		fmt.Fprintf(&b, "%s\n", bc.Line())
	}
	for _, md := range s.Metadata {
		fmt.Fprintf(&b, "meta field=%s source=%s value=%s conflict=%v candidates=%s\n",
			md.Field, md.Source, md.Value, md.Conflict, strings.Join(md.Candidates, ","))
	}
	fixes := append([]repair.TimestampFix(nil), s.Fixes...)
	sort.SliceStable(fixes, func(i, j int) bool {
		if fixes[i].TrackID != fixes[j].TrackID {
			return fixes[i].TrackID < fixes[j].TrackID
		}
		return fixes[i].SampleNumber < fixes[j].SampleNumber
	})
	for _, f := range fixes {
		fmt.Fprintf(&b, "fix track=%d n=%d kind=%s before_us=%d after_us=%d before_dur_us=%d after_dur_us=%d\n",
			f.TrackID, f.SampleNumber, f.Kind,
			f.BeforeDTS.Micros(), f.AfterDTS.Micros(), f.BeforeDur.Micros(), f.AfterDur.Micros())
	}
	return []byte(b.String())
}

// Digest is the 64-character lowercase hex SHA-256 of Canonical.
func (s *Summary) Digest() string {
	sum := sha256.Sum256(s.Canonical())
	return hex.EncodeToString(sum[:])
}

// WriteText prints the human-readable report. Every line is stable so two
// runs can be diffed directly.
func (s *Summary) WriteText(w io.Writer) error {
	bw := &errWriter{w: w}
	fmt.Fprintf(bw, "replaycheck proof seed=%d workers=%d queue=%d resumed=%v canceled=%v\n",
		s.Seed, s.Workers, s.QueueSize, s.Resumed, s.Canceled)
	for _, m := range s.Maps {
		fmt.Fprintln(bw, m.Line())
	}
	for _, md := range s.Metadata {
		fmt.Fprintf(bw, "meta field=%s source=%s value=%s conflict=%v candidates=%s\n",
			md.Field, md.Source, md.Value, md.Conflict, strings.Join(md.Candidates, ","))
	}
	for _, f := range s.Fixes {
		fmt.Fprintf(bw, "fix track=%d n=%d kind=%s before_us=%d after_us=%d\n",
			f.TrackID, f.SampleNumber, f.Kind, f.BeforeDTS.Micros(), f.AfterDTS.Micros())
	}
	for _, c := range s.Clips {
		fmt.Fprintf(bw, "clip event=%d path=%s bytes=%d sha256=%s samples_video=%d samples_audio=%d start_us=%d end_us=%d deviation_us=%d\n",
			c.Spec.EventIndex, c.Path, c.Bytes, c.Hash,
			len(c.Spec.Video), len(c.Spec.Audio), c.Spec.Start.Micros(), c.Spec.End.Micros(), c.Spec.Deviation.Micros())
	}
	for _, bc := range s.BadCases {
		fmt.Fprintln(bw, bc.Line())
	}
	fmt.Fprintln(bw, s.KeyMetrics())
	fmt.Fprintf(bw, "digest_sha256=%s\n", s.Digest())
	return bw.err
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	n, err := e.w.Write(p)
	e.err = err
	return n, err
}

// MapFromEntry builds a sample map record from an index entry and the clip
// mapping result.
func MapFromEntry(e index.Entry, clipName string, clipShift media.Rational, clipOffset int64, clipTime media.Rational) SampleMap {
	m := SampleMap{
		TrackID: e.TrackID, Number: e.Number, Source: e.DTS,
		Duration: e.End.Sub(e.DTS), ClipName: clipName,
		SourceOffset: e.Offset, Size: e.Size, Keyframe: e.Keyframe,
	}
	if clipName != "" {
		m.ClipTime = clipTime
		m.ClipOffset = clipOffset
		m.TimeError = clipTime.Add(clipShift).Sub(e.DTS).Abs().MicrosCeil()
	}
	return m
}
