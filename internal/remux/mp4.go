// Package remux writes demuxed samples into an MP4 file without touching the
// compressed unit bytes. Annex B start codes are replaced by the 4-byte length
// prefixes that MP4 sample entries require; NAL payload bytes are copied
// verbatim. AV1 OBU samples are stored byte-for-byte.
package remux

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"mediadist/internal/codec"
	"mediadist/internal/demux"
	"mediadist/internal/merr"
)

// TrackPlan is one output track: its samples and codec parameters.
type TrackPlan struct {
	TrackID   uint32
	Codec     demux.Codec
	Samples   []demux.Sample
	Timescale uint32
	Width     uint16
	Height    uint16
	Handler   [4]byte
	// SampleEntry is avc1, hvc1, av01 or mp4a.
	SampleEntry [4]byte
	// ConfigEntry is avcC, hvcC or av1C.
	ConfigEntry [4]byte
	// ConfigRecord is the decoder configuration payload copied verbatim.
	ConfigRecord []byte
}

// Options controls layout and limits.
type Options struct {
	MovieTimescale uint32
	MaxSamples     int
	MaxTotalBytes  int64
	ProductName    string
}

// DefaultOptions uses a millisecond movie timescale.
func DefaultOptions() Options {
	return Options{MovieTimescale: 1000, MaxSamples: 200_000, MaxTotalBytes: 256 << 20, ProductName: "mediadist"}
}

func (o Options) normalize() Options {
	d := DefaultOptions()
	if o.MovieTimescale == 0 {
		o.MovieTimescale = d.MovieTimescale
	}
	if o.MaxSamples <= 0 {
		o.MaxSamples = d.MaxSamples
	}
	if o.MaxTotalBytes <= 0 {
		o.MaxTotalBytes = d.MaxTotalBytes
	}
	if o.ProductName == "" {
		o.ProductName = d.ProductName
	}
	return o
}

// SampleEvidence proves one output sample's content.
type SampleEvidence struct {
	TrackID   uint32 `json:"track_id"`
	Index     int    `json:"index"`
	PTS       int64  `json:"pts"`
	Dur       int64  `json:"dur"`
	Size      int    `json:"size"`
	SHA256    string `json:"sha256"`
	CanonHash string `json:"canon_sha256"`
	Keyframe  bool   `json:"keyframe"`
	Damaged   bool   `json:"damaged"`
	Segment   int    `json:"segment"`
	MP4Offset int64  `json:"mp4_offset"`
}

// Report is the remux result.
type Report struct {
	Path           string           `json:"path"`
	Size           int64            `json:"size"`
	SampleCount    int              `json:"sample_count"`
	MovieTimescale uint32           `json:"movie_timescale"`
	Evidence       []SampleEvidence `json:"evidence"`
	SHA256         string           `json:"sha256"`
	Errors         []*merr.Error    `json:"-"`
}

// layout carries the per-track sample table decision.
type layout struct {
	plan        TrackPlan
	samples     []demux.Sample
	stts        []mp4.SttsEntry
	stss        []uint32
	sizes       []uint32
	offsets     []uint32
	chunkOffset uint32
	bytes       []byte // concatenated mdat payload for this track
	duration    uint64
	canon       []string
}

// Build validates sample counts and byte totals before writing.
func Build(plans []TrackPlan, opts Options) (int, int64, []*merr.Error) {
	opts = opts.normalize()
	var errs []*merr.Error
	count := 0
	var total int64
	for _, p := range plans {
		if p.TrackID == 0 {
			errs = append(errs, merr.New(merr.StageRemux, "zero_track", merr.NoSpan(0), false,
				"track id 0 cannot be written"))
		}
		if p.Timescale == 0 {
			errs = append(errs, merr.New(merr.StageRemux, "zero_timescale",
				merr.Span{Stream: 0, Seq: -1, Offset: -1, TrackID: p.TrackID}, false,
				"track %d has timescale 0", p.TrackID))
		}
		count += len(p.Samples)
		for _, s := range p.Samples {
			total += int64(len(s.Data))
		}
	}
	if count > opts.MaxSamples {
		errs = append(errs, merr.New(merr.StageRemux, "sample_limit", merr.NoSpan(0), false,
			"%d samples exceed limit %d", count, opts.MaxSamples))
	}
	if total > opts.MaxTotalBytes {
		errs = append(errs, merr.New(merr.StageRemux, "byte_limit", merr.NoSpan(0), false,
			"%d sample bytes exceed limit %d", total, opts.MaxTotalBytes))
	}
	return count, total, errs
}

// payloadFor converts one demuxed sample to its MP4 storage bytes. NAL
// payload bytes are unchanged; only the framing differs. The returned canon
// hash covers the concatenated NAL payloads with no framing at all, so a
// reader can prove the transport framing did not alter media either.
func payloadFor(codecName demux.Codec, data []byte) (stored []byte, canon string, err error) {
	switch codecName {
	case demux.CodecH264, demux.CodecH265:
		scan := scanAnnexB(codecName, data)
		out := make([]byte, 0, len(data))
		h := sha256.New()
		for _, u := range scan.Units {
			payloadStart := u.Offset + u.HeaderLen
			end := payloadStart + u.Size
			if end > int64(len(data)) {
				end = int64(len(data))
			}
			if payloadStart >= end {
				continue
			}
			var lenBuf [4]byte
			binary.BigEndian.PutUint32(lenBuf[:], uint32(end-payloadStart))
			out = append(out, lenBuf[:]...)
			out = append(out, data[payloadStart:end]...)
			h.Write(data[payloadStart:end])
		}
		if len(out) == 0 {
			return nil, "", merr.New(merr.StageRemux, "no_units",
				merr.OffsetSpan(0, 0, int64(len(data))), true,
				"sample has no parsable NAL unit")
		}
		return out, fmt.Sprintf("%x", h.Sum(nil)), nil
	default:
		sum := sha256.Sum256(data)
		return append([]byte(nil), data...), fmt.Sprintf("%x", sum[:]), nil
	}
}

func scanAnnexB(c demux.Codec, data []byte) codec.ScanResult {
	if c == demux.CodecH265 {
		return codec.ScanH265(0, data, codec.DefaultScanLimits())
	}
	return codec.ScanH264(0, data, codec.DefaultScanLimits())
}

// newLayout validates ascending presentation order and builds stts runs and
// byte layout. Order is preserved rather than re-sorted so a timeline defect
// surfaces as an error instead of being hidden by a reorder.
func newLayout(p TrackPlan) (*layout, error) {
	l := &layout{plan: p}
	l.samples = append([]demux.Sample(nil), p.Samples...)
	prev := int64(-1)
	for i, s := range l.samples {
		if s.PTS < 0 {
			return nil, merr.New(merr.StageRemux, "negative_pts",
				merr.Span{Stream: s.Stream, Seq: -1, Offset: -1, TrackID: p.TrackID}, false,
				"sample %d pts %d is negative", i, s.PTS)
		}
		if prev >= 0 && s.PTS <= prev {
			code := "non_monotonic"
			if s.PTS == prev {
				code = "duplicate_pts"
			}
			return nil, merr.New(merr.StageRemux, code,
				merr.Span{Stream: s.Stream, Seq: -1, Offset: -1, TrackID: p.TrackID}, false,
				"sample %d pts %d not after %d", i, s.PTS, prev)
		}
		prev = s.PTS
		payload, canon, err := payloadFor(p.Codec, s.Data)
		if err != nil {
			e := merr.AsError(err)
			e.Span.TrackID = p.TrackID
			return nil, e
		}
		l.bytes = append(l.bytes, payload...)
		l.sizes = append(l.sizes, uint32(len(payload)))
		l.canon = append(l.canon, canon)
		if s.Keyframe {
			l.stss = append(l.stss, uint32(i+1))
		}
	}
	var runCount, runDelta uint32
	for i, s := range l.samples {
		var dur int64
		if i+1 < len(l.samples) {
			dur = l.samples[i+1].PTS - s.PTS
		} else if s.Dur > 0 {
			dur = s.Dur
		}
		if dur <= 0 {
			dur = 1
		}
		dur32 := uint32(dur)
		if runCount == 0 {
			runDelta, runCount = dur32, 1
			continue
		}
		if dur32 == runDelta {
			runCount++
			continue
		}
		l.stts = append(l.stts, mp4.SttsEntry{SampleCount: runCount, SampleDelta: runDelta})
		runDelta, runCount = dur32, 1
	}
	if runCount > 0 {
		l.stts = append(l.stts, mp4.SttsEntry{SampleCount: runCount, SampleDelta: runDelta})
		for _, e := range l.stts {
			l.duration += uint64(e.SampleCount) * uint64(e.SampleDelta)
		}
	}
	return l, nil
}

// WriteFile writes plans to dir/name atomically through a temp file in dir.
func WriteFile(dir, name string, plans []TrackPlan, opts Options) (*Report, error) {
	opts = opts.normalize()
	rep := &Report{MovieTimescale: opts.MovieTimescale}
	if _, _, errs := Build(plans, opts); len(errs) > 0 {
		rep.Errors = errs
		return rep, errs[0]
	}
	var layouts []*layout
	for _, p := range plans {
		if len(p.Samples) == 0 {
			continue
		}
		l, err := newLayout(p)
		if err != nil {
			return rep, err
		}
		layouts = append(layouts, l)
	}
	if len(layouts) == 0 {
		return rep, merr.New(merr.StageRemux, "no_samples", merr.NoSpan(0), false, "nothing to write")
	}
	tmp, err := os.CreateTemp(dir, "mediadist-*.mp4")
	if err != nil {
		return rep, merr.Wrap(merr.StageRemux, "temp_failed", merr.NoSpan(0), false, err, "creating temp file")
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	w := mp4.NewWriter(tmp)
	if err := writeFtyp(w); err != nil {
		return rep, err
	}
	patches, err := writeMoov(w, layouts, opts)
	if err != nil {
		return rep, err
	}
	if err := writeMdat(tmp, w, layouts, patches); err != nil {
		return rep, err
	}
	if err := tmp.Sync(); err != nil {
		return rep, merr.Wrap(merr.StageRemux, "sync_failed", merr.NoSpan(0), false, err, "syncing output")
	}
	final := name
	if dir != "" {
		final = dir + string(os.PathSeparator) + name
	}
	if err := tmp.Close(); err != nil {
		return rep, merr.Wrap(merr.StageRemux, "close_failed", merr.NoSpan(0), false, err, "closing output")
	}
	sum, size, err := hashFile(tmpName)
	if err != nil {
		return rep, err
	}
	if err := os.Rename(tmpName, final); err != nil {
		return rep, merr.Wrap(merr.StageRemux, "rename_failed", merr.NoSpan(0), false, err, "moving output into place")
	}
	committed = true
	rep.Path = final
	rep.Size = size
	rep.SHA256 = sum
	rep.Evidence = collectEvidence(layouts)
	rep.SampleCount = len(rep.Evidence)
	return rep, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, merr.Wrap(merr.StageRemux, "hash_open", merr.NoSpan(0), false, err, "opening output")
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, merr.Wrap(merr.StageRemux, "hash_read", merr.NoSpan(0), false, err, "hashing output")
	}
	return fmt.Sprintf("%x", h.Sum(nil)), n, nil
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

func wrapWrite(code string, err error) error {
	if err == nil {
		return nil
	}
	if e := merr.AsError(err); e != nil {
		return err
	}
	return merr.Wrap(merr.StageRemux, code, merr.NoSpan(0), false, err, "mp4 write failed")
}
