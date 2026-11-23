// Package index builds and persists the per-track processing index.
//
// An index is bound to the SHA-256 digest of the exact input bytes that
// produced it. Loading an index whose digest disagrees with the input is
// refused with a located fault, so stale metadata can never drive a run.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"mediatool/internal/audio"
	"mediatool/internal/clock"
	"mediatool/internal/fault"
	"mediatool/internal/rtp"
	"mediatool/internal/video"
)

// Version is the on-disk index schema version.
const Version = 1

// DigestReader returns the SHA-256 of everything it reads.
func DigestReader(r io.Reader) (io.Reader, func() string) {
	h := sha256.New()
	return io.TeeReader(r, h), func() string { return hex.EncodeToString(h.Sum(nil)) }
}

// DigestBytes returns the lowercase hex SHA-256 of b.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PacketRange locates a track's packets in the input.
type PacketRange struct {
	FirstOffset int64  `json:"first_offset"`
	LastOffset  int64  `json:"last_offset"`
	FirstExtSeq uint64 `json:"first_ext_seq"`
	LastExtSeq  uint64 `json:"last_ext_seq"`
	Packets     int    `json:"packets"`
}

// TimeRange is the media-time span observed for a track.
type TimeRange struct {
	Valid   bool  `json:"valid"`
	StartMs int64 `json:"start_ms"`
	EndMs   int64 `json:"end_ms"`
}

// Keyframe records where a decodable random-access point begins.
type Keyframe struct {
	Offset  int64  `json:"offset"`
	ExtSeq  uint64 `json:"ext_seq"`
	Seq     uint16 `json:"seq"`
	MediaMs int64  `json:"media_ms"`
}

// AudioInfo is the normalized audio description of a track.
type AudioInfo struct {
	Layout     string `json:"layout"`
	Channels   int    `json:"channels"`
	SampleRate int    `json:"sample_rate"`
	// OutputLayout and OutputSampleRate describe the normalization target.
	OutputLayout     string `json:"output_layout,omitempty"`
	OutputSampleRate int    `json:"output_sample_rate,omitempty"`
	// Map is the explicit channel mapping applied, if any.
	ChannelMap []int `json:"channel_map,omitempty"`
	Dropped    []int `json:"dropped_channels,omitempty"`
}

// VideoInfo is the normalized video description of a track.
type VideoInfo struct {
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	Color      string `json:"color"`
	ColorKnown bool   `json:"color_known"`
	ColorSrc   string `json:"color_source,omitempty"`
}

// Track is the indexed state of one RTP track.
type Track struct {
	TrackID   uint32      `json:"track_id"`
	SSRC      uint32      `json:"ssrc"`
	Kind      string      `json:"kind"` // "audio" or "video"
	ClockRate uint32      `json:"clock_rate"`
	Packets   PacketRange `json:"packets"`
	Time      TimeRange   `json:"time"`
	// BaseNTPNs is the absolute NTP instant of the track's media zero, used
	// to compare media times against untrusted NTP intervals.
	BaseNTPNs uint64            `json:"base_ntp_ns,omitempty"`
	Keyframes []Keyframe        `json:"keyframes,omitempty"`
	Audio     *AudioInfo        `json:"audio,omitempty"`
	Video     *VideoInfo        `json:"video,omitempty"`
	Untrusted []clock.Untrusted `json:"untrusted,omitempty"`
	Truncated int               `json:"truncated_packets"`
	Lost      int               `json:"lost_packets"`
	Reordered int               `json:"reordered_packets"`
	Duplicate int               `json:"duplicate_packets"`
}

// FaultRecord is a located fault captured during the scan. It is part of the
// index so a resumed run reports the same damage it saw the first time.
type FaultRecord struct {
	Kind     string `json:"kind"`
	TrackID  uint32 `json:"track_id,omitempty"`
	Offset   int64  `json:"offset"`
	Length   int    `json:"length,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Recovery string `json:"recovery"`
}

// Index is the complete processing index for one input.
type Index struct {
	Version     int               `json:"version"`
	InputDigest string            `json:"input_digest"`
	InputBytes  int64             `json:"input_bytes"`
	Window      int               `json:"window"`
	Tracks      []Track           `json:"tracks"`
	Stats       rtp.Stats         `json:"stats"`
	Untrusted   []clock.Untrusted `json:"untrusted,omitempty"`
	Faults      []FaultRecord     `json:"faults,omitempty"`
	// Checksum is the SHA-256 of the canonical JSON with Checksum empty.
	Checksum string `json:"checksum"`
}

// Config configures index construction.
type Config struct {
	// Window is the scanner reorder window, recorded for reproducibility.
	Window int
	// MaxKeyframesPerTrack bounds keyframe retention. Zero means 4096.
	MaxKeyframesPerTrack int
	// TrackKind maps track IDs to "audio"/"video".
	TrackKind map[uint32]string
	// TrackOrder is the stable track ordering; when empty, tracks sort by ID.
	TrackOrder []uint32
}

func (c Config) maxKeyframes() int {
	if c.MaxKeyframesPerTrack <= 0 {
		return 4096
	}
	return c.MaxKeyframesPerTrack
}

// Builder accumulates scan events into an index.
type Builder struct {
	cfg     Config
	digest  string
	bytes   int64
	tracks  map[uint32]*Track
	stats   rtp.Stats
	untrust []clock.Untrusted
	faults  []*fault.Fault
}

// NewBuilder creates a builder bound to the input digest.
func NewBuilder(digest string, cfg Config) *Builder {
	return &Builder{cfg: cfg, digest: digest, tracks: make(map[uint32]*Track)}
}

// Track returns the mutable track record, creating it on first use.
func (b *Builder) Track(id uint32) *Track {
	t := b.tracks[id]
	if t == nil {
		kind := b.cfg.TrackKind[id]
		if kind == "" {
			kind = "unknown"
		}
		t = &Track{TrackID: id, Kind: kind}
		b.tracks[id] = t
	}
	return t
}

// AddEvent folds one scanner event into the index. stamp may be invalid when
// no trusted sender report exists yet; the time range stays unset in that case.
func (b *Builder) AddEvent(ev *rtp.Event, stamp clock.Stamped) {
	switch ev.Kind {
	case rtp.EventPacket:
		if ev.TrackID == 0 && ev.SSRC != 0 {
			// Unknown-SSRC packets still count toward the input digest but do
			// not belong to a configured track.
			break
		}
		t := b.Track(ev.TrackID)
		t.SSRC = ev.SSRC
		if t.ClockRate == 0 {
			t.ClockRate = b.clockRateFor(ev.TrackID)
		}
		if t.Packets.Packets == 0 {
			t.Packets.FirstOffset = ev.Offset
			t.Packets.FirstExtSeq = ev.ExtSeq
		}
		t.Packets.LastOffset = ev.Offset
		t.Packets.LastExtSeq = ev.ExtSeq
		t.Packets.Packets++
		if ev.OriginalLength > ev.Length {
			t.Truncated++
		}
		if stamp.Valid {
			if !t.Time.Valid {
				t.Time = TimeRange{Valid: true, StartMs: stamp.MediaMs, EndMs: stamp.MediaMs}
			}
			if stamp.MediaMs < t.Time.StartMs {
				t.Time.StartMs = stamp.MediaMs
			}
			if stamp.MediaMs > t.Time.EndMs {
				t.Time.EndMs = stamp.MediaMs
			}
		}
		if ev.Keyframe && stamp.Valid {
			// A keyframe with no trusted clock has no placeable media time; it
			// is not recorded at a fabricated zero.
			if len(t.Keyframes) < b.cfg.maxKeyframes() {
				t.Keyframes = append(t.Keyframes, Keyframe{
					Offset: ev.Offset, ExtSeq: ev.ExtSeq, Seq: ev.Seq, MediaMs: stamp.MediaMs,
				})
			}
		}
		for _, f := range ev.Faults {
			switch f.Kind {
			case fault.KindLoss:
				t.Lost++
			case fault.KindReordered:
				t.Reordered++
			case fault.KindDuplicate:
				t.Duplicate++
			}
		}
	case rtp.EventSenderReport:
		if ev.SR == nil {
			break
		}
		t := b.Track(ev.SR.TrackID)
		t.SSRC = ev.SR.SSRC
		if t.ClockRate == 0 {
			t.ClockRate = ev.SR.ClockRate
		}
	case rtp.EventFault:
	}
	for _, f := range ev.Faults {
		b.faults = append(b.faults, f)
	}
}

func (b *Builder) clockRateFor(id uint32) uint32 {
	return 0 // filled by RTCP or by the caller's track metadata
}

// SetAudio attaches normalized audio metadata to a track.
func (b *Builder) SetAudio(trackID uint32, info AudioInfo) { b.Track(trackID).Audio = &info }

// SetVideo attaches normalized video metadata to a track.
func (b *Builder) SetVideo(trackID uint32, info VideoInfo) { b.Track(trackID).Video = &info }

// SetClockRate records a track's RTP clock rate.
func (b *Builder) SetClockRate(trackID uint32, rate uint32) {
	t := b.Track(trackID)
	t.ClockRate = rate
}

// SetStats records scanner totals.
func (b *Builder) SetStats(s rtp.Stats) { b.stats = s }

// SetInputBytes records the input length.
func (b *Builder) SetInputBytes(n int64) { b.bytes = n }

// SetUntrusted records the timeline's untrusted intervals.
func (b *Builder) SetUntrusted(u []clock.Untrusted) { b.untrust = u }

// Build finalizes the index with a deterministic track order and checksum.
func (b *Builder) Build() *Index {
	ids := make([]uint32, 0, len(b.tracks))
	for id := range b.tracks {
		ids = append(ids, id)
	}
	order := b.cfg.TrackOrder
	if len(order) == 0 {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	} else {
		ids = append([]uint32(nil), order...)
	}
	ix := &Index{
		Version:     Version,
		InputDigest: b.digest,
		InputBytes:  b.bytes,
		Window:      b.cfg.Window,
		Stats:       b.stats,
		Untrusted:   b.untrust,
	}
	for _, id := range ids {
		t := b.tracks[id]
		if t == nil {
			continue
		}
		sort.SliceStable(t.Keyframes, func(i, j int) bool {
			if t.Keyframes[i].ExtSeq != t.Keyframes[j].ExtSeq {
				return t.Keyframes[i].ExtSeq < t.Keyframes[j].ExtSeq
			}
			return t.Keyframes[i].Offset < t.Keyframes[j].Offset
		})
		ix.Tracks = append(ix.Tracks, *t)
	}
	for _, f := range b.faults {
		ix.Faults = append(ix.Faults, FaultRecord{
			Kind: string(f.Kind), TrackID: f.TrackID, Offset: f.Offset,
			Length: f.Length, Detail: f.Detail, Recovery: string(f.Recovery),
		})
	}
	// Faults are sorted by input position so the index is stable regardless of
	// which window first observed them.
	sort.SliceStable(ix.Faults, func(i, j int) bool {
		if ix.Faults[i].Offset != ix.Faults[j].Offset {
			return ix.Faults[i].Offset < ix.Faults[j].Offset
		}
		return ix.Faults[i].Kind < ix.Faults[j].Kind
	})
	ix.Checksum = ix.checksum()
	return ix
}

// canonicalJSON marshals with Checksum cleared. Struct field order is fixed,
// so the bytes are deterministic across runs and platforms.
func (ix *Index) canonicalJSON() ([]byte, error) {
	cp := *ix
	cp.Checksum = ""
	return json.Marshal(&cp)
}

func (ix *Index) checksum() string {
	b, err := ix.canonicalJSON()
	if err != nil {
		return ""
	}
	return DigestBytes(b)
}

// Encode serializes the index with a recomputed checksum.
func (ix *Index) Encode() ([]byte, error) {
	cp := *ix
	cp.Checksum = cp.checksum()
	return json.MarshalIndent(&cp, "", "  ")
}

// Save writes the index atomically: a temporary file is renamed into place so
// a crash never leaves a half-written index.
func (ix *Index) Save(path string) error {
	data, err := ix.Encode()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".index-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Load reads an index and verifies its checksum and input digest.
func Load(path, wantDigest string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(data, &ix); err != nil {
		return nil, fmt.Errorf("decode index %s: %w", path, err)
	}
	if ix.Version != Version {
		return nil, (&fault.Fault{Kind: fault.KindIndexDigest, Offset: 0,
			Recovery: fault.RecoverRestart, Severity: fault.SeverityFatal}).
			WithDetail("index version %d, want %d", ix.Version, Version)
	}
	if got := ix.checksum(); got != ix.Checksum {
		return nil, (&fault.Fault{Kind: fault.KindIndexDigest, Offset: 0,
			Recovery: fault.RecoverRestart, Severity: fault.SeverityFatal}).
			WithDetail("index checksum mismatch: stored %s computed %s", ix.Checksum, got)
	}
	if wantDigest != "" && ix.InputDigest != wantDigest {
		return nil, (&fault.Fault{Kind: fault.KindIndexDigest, Offset: 0,
			Recovery: fault.RecoverRestart, Severity: fault.SeverityFatal}).
			WithDetail("index bound to input %s but input is %s; rescan required", short(ix.InputDigest), short(wantDigest))
	}
	return &ix, nil
}

func short(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// Track returns the indexed track with the given ID.
func (ix *Index) Track(id uint32) (*Track, bool) {
	for i := range ix.Tracks {
		if ix.Tracks[i].TrackID == id {
			return &ix.Tracks[i], true
		}
	}
	return nil, false
}

// KeyframeAtOrAfter returns the first keyframe at or after mediaMs.
func (t *Track) KeyframeAtOrAfter(mediaMs int64) (Keyframe, bool) {
	for _, k := range t.Keyframes {
		if k.MediaMs >= mediaMs {
			return k, true
		}
	}
	return Keyframe{}, false
}

// NormalizeAudio resolves a layout name and builds the explicit output map.
func NormalizeAudio(trackID uint32, layoutName string, sampleRate int, outLayout string, outRate int, offset int64) (*AudioInfo, error) {
	from, err := audio.Lookup(layoutName)
	if err != nil {
		return nil, audio.UnknownLayoutFault(layoutName, trackID, offset)
	}
	info := &AudioInfo{Layout: from.Name, Channels: from.Len(), SampleRate: sampleRate, OutputLayout: from.Name, OutputSampleRate: sampleRate}
	if outLayout != "" {
		to, err := audio.Lookup(outLayout)
		if err != nil {
			return nil, audio.UnknownLayoutFault(outLayout, trackID, offset)
		}
		cm, err := audio.DefaultMap(from, to)
		if err != nil {
			return nil, audio.Locate(err, trackID, offset)
		}
		info.OutputLayout = to.Name
		info.ChannelMap = cm.Index
		info.Dropped = cm.Dropped
	}
	if outRate != 0 && outRate != sampleRate {
		if _, err := audio.NewRateConverter(sampleRate, outRate); err != nil {
			return nil, audio.Locate(err, trackID, offset)
		}
		info.OutputSampleRate = outRate
	}
	return info, nil
}

// NormalizeVideo resolves color candidates into index metadata.
func NormalizeVideo(trackID uint32, width, height int, candidates []video.Candidate) (VideoInfo, error) {
	dec, err := video.Normalize(candidates)
	vi := VideoInfo{Width: width, Height: height, Color: dec.Chosen.String(), ColorKnown: dec.Chosen.Known, ColorSrc: dec.Chosen.Source}
	if err != nil {
		if f, ok := fault.As(err); ok {
			g := *f
			g.TrackID = trackID
			err = &g
		}
	}
	return vi, err
}

// SetAudio attaches audio metadata to an already-built index.
func (ix *Index) SetAudio(trackID uint32, info AudioInfo) {
	if tr, ok := ix.Track(trackID); ok {
		tr.Audio = &info
	}
	ix.Checksum = ix.checksum()
}

// SetVideo attaches video metadata to an already-built index.
func (ix *Index) SetVideo(trackID uint32, info VideoInfo) {
	if tr, ok := ix.Track(trackID); ok {
		tr.Video = &info
	}
	ix.Checksum = ix.checksum()
}
