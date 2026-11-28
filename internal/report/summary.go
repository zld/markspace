// Package report turns pipeline results into quantitative, reproducible
// summaries. Frames are divided into fixed per-track time shards, each hashed
// with SHA-256 over deterministic content, so the same frame set always yields
// identical shard and summary hashes regardless of arrival order.
package report

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"

	"medcheck/internal/index"
	"medcheck/internal/model"
	"medcheck/internal/repair"
	"medcheck/internal/timeline"
)

// Shard is one fixed (track, time boundary) hash over its member frames.
type Shard struct {
	Track        uint32
	Bucket       int64 // start time of the fixed window, in microseconds
	WindowMicros int64
	FrameCount   int
	SHA256       string
}

// Summary is the full quantitative result of one pipeline run.
type Summary struct {
	FrameCount      int
	ValidFrames     int
	InvalidFrames   int
	BoundaryCount   int
	KeyframeCount   int
	KeyframeHits    int
	KeyframeMisses  int
	BadSegments     int
	Shards          []Shard
	SegmentHashes   []string
	WatermarkMicros int64
	MaxEventMicros  int64
}

// shardBucket maps an event time to its fixed-window start.
func shardBucket(micros, window int64) int64 {
	if window <= 0 {
		return 0
	}
	if micros >= 0 {
		return (micros / window) * window
	}
	return ((micros - window + 1) / window) * window
}

// frameDigest is the order-stable byte representation of one frame.
func frameDigest(h interface{ Write([]byte) (int, error) }, f *model.Frame) {
	u32 := func(v uint32) {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], v)
		h.Write(b[:])
	}
	i64 := func(v int64) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(v))
		h.Write(b[:])
	}
	u32(f.Track)
	u32(f.Source)
	u32(f.Seq)
	i64(f.EventTime.MicrosOr(f.EventTime.Ticks))
	i64(f.Offset.Start)
	i64(f.Offset.End)
	u32(f.PayloadLen)
	u32(uint32(f.Type))
	if f.Valid {
		u32(1)
	} else {
		u32(0)
	}
}

// BuildShards partitions frames into fixed per-track time windows and hashes
// each deterministically.
func BuildShards(frames []*model.Frame, windowMicros int64) []Shard {
	type key struct {
		track  uint32
		bucket int64
	}
	groups := map[key][]*model.Frame{}
	var keys []key
	for _, f := range frames {
		micros := f.EventTime.MicrosOr(f.EventTime.Ticks)
		k := key{f.Track, shardBucket(micros, windowMicros)}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], f)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].track != keys[j].track {
			return keys[i].track < keys[j].track
		}
		return keys[i].bucket < keys[j].bucket
	})
	shards := make([]Shard, 0, len(keys))
	for _, k := range keys {
		members := groups[k]
		sort.SliceStable(members, func(i, j int) bool {
			mi := members[i].EventTime.MicrosOr(members[i].EventTime.Ticks)
			mj := members[j].EventTime.MicrosOr(members[j].EventTime.Ticks)
			if mi != mj {
				return mi < mj
			}
			if members[i].Seq != members[j].Seq {
				return members[i].Seq < members[j].Seq
			}
			return members[i].Offset.Start < members[j].Offset.Start
		})
		h := sha256.New()
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], k.track)
		h.Write(b[:])
		var b8 [8]byte
		binary.BigEndian.PutUint64(b8[:], uint64(k.bucket))
		h.Write(b8[:])
		for _, f := range members {
			frameDigest(h, f)
		}
		shards = append(shards, Shard{
			Track:        k.track,
			Bucket:       k.bucket,
			WindowMicros: windowMicros,
			FrameCount:   len(members),
			SHA256:       hex.EncodeToString(h.Sum(nil)),
		})
	}
	return shards
}

// Inputs are the derived artifacts summarized together.
type Inputs struct {
	View         *timeline.View
	Index        *index.KeyframeIndex
	Segments     []*repair.Segment
	WindowMicros int64
}

// Build assembles the summary from all derived artifacts.
func Build(in Inputs) *Summary {
	s := &Summary{}
	if in.View != nil {
		s.FrameCount = in.View.FrameCount
		s.WatermarkMicros = in.View.WatermarkMicros
		s.MaxEventMicros = in.View.MaxEventMicros
		s.BoundaryCount = in.View.BoundaryCount()
		for _, f := range in.View.Ordered {
			if f.Valid && f.Err == nil {
				s.ValidFrames++
			} else {
				s.InvalidFrames++
			}
		}
		s.Shards = BuildShards(in.View.Ordered, in.WindowMicros)
	}
	if in.Index != nil {
		s.KeyframeCount = in.Index.Len()
	}
	s.BadSegments = len(in.Segments)
	for _, seg := range in.Segments {
		s.SegmentHashes = append(s.SegmentHashes, seg.SHA256)
	}
	return s
}

// Hash is a single reproducible digest of the whole summary. Two runs over the
// same frames must match even when arrival order differed.
func (s *Summary) Hash() string {
	h := sha256.New()
	i64 := func(v int64) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(v))
		h.Write(b[:])
	}
	u32 := func(v uint32) {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], v)
		h.Write(b[:])
	}
	writeStr := func(v string) {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(len(v)))
		h.Write(b[:])
		h.Write([]byte(v))
	}
	u32(uint32(s.FrameCount))
	u32(uint32(s.ValidFrames))
	u32(uint32(s.InvalidFrames))
	u32(uint32(s.BoundaryCount))
	u32(uint32(s.KeyframeCount))
	u32(uint32(s.BadSegments))
	i64(s.WatermarkMicros)
	i64(s.MaxEventMicros)
	for _, sh := range s.Shards {
		u32(sh.Track)
		i64(sh.Bucket)
		u32(uint32(sh.FrameCount))
		writeStr(sh.SHA256)
	}
	// Segment hashes are sorted so ordering cannot leak into the digest.
	segHashes := append([]string{}, s.SegmentHashes...)
	sort.Strings(segHashes)
	for _, v := range segHashes {
		writeStr(v)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RecordKeyframeQuery marks a hit or miss for the acceptance metrics.
func (s *Summary) RecordKeyframeQuery(hit bool) {
	if hit {
		s.KeyframeHits++
	} else {
		s.KeyframeMisses++
	}
}

// KeyframeHitRate returns hits/(hits+misses), or 1 when no query was made.
func (s *Summary) KeyframeHitRate() float64 {
	total := s.KeyframeHits + s.KeyframeMisses
	if total == 0 {
		return 1
	}
	return float64(s.KeyframeHits) / float64(total)
}
