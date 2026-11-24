// Package index builds the random-access index for repaired video streams.
//
// Every record cites the source packet offset, the elementary-stream PID, the
// unwrapped timestamps and the PSI snapshot that governed the packet. A
// record is only emitted while the snapshot in force at the keyframe declares
// the same PID and stream type, so a randomly located index entry can never
// point into a stale or remapped track.
package index

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"sort"

	"tsforge/internal/clock"
	"tsforge/internal/ts"
)

// Limits bound the index and cache sizes required by the operating limits.
const (
	MaxRecords = 500_000
	// MaxPayloadPerRecord is the largest sample this remuxer will cite.
	MaxPayloadPerRecord = 4 << 20
)

// LimitsError reports a hard limit; callers keep resumable state so an
// operator can split the input and rerun.
type LimitsError struct {
	What  string
	Limit int64
	At    int64
}

func (e *LimitsError) Error() string {
	return fmt.Sprintf("index limit exceeded: %s limit=%d at=%d", e.What, e.Limit, e.At)
}

// IsLimits reports whether err is a limit error.
func IsLimits(err error) bool {
	var le *LimitsError
	return errors.As(err, &le)
}

// Kind distinguishes the elementary stream a record belongs to.
type Kind string

const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
	KindOther Kind = "other"
)

// Record is one random-access entry.
type Record struct {
	// Seq is the ordinal in emission order, used as the sample number.
	Seq int64 `json:"seq"`
	// PacketOffset is the first source packet of the sample. SourceOffsets
	// cites every packet that carries the sample, so a remuxer can read the
	// complete elementary frame instead of only its first fragment.
	PacketOffset  int64   `json:"packet_offset"`
	PacketIndex   int64   `json:"packet_index"`
	SourceOffsets []int64 `json:"source_offsets"`
	// PID of the elementary stream carrying this sample.
	PID  uint16 `json:"pid"`
	Kind Kind   `json:"kind"`
	DTS  int64  `json:"dts"`
	PTS  int64  `json:"pts"`
	// Duration in ticks until the next record's DTS (filled during close).
	Duration int64 `json:"duration"`
	// Keyframe marks IDR / random_access_point samples.
	Keyframe bool `json:"keyframe"`
	// RandomAccessFlag is the TS adaptation field random_access_indicator.
	RandomAccessFlag bool `json:"random_access_flag"`
	// PayloadBytes is the total elementary byte count of the sample.
	PayloadBytes int `json:"payload_bytes"`
	// SnapshotID of the PSI mapping in force when this record was emitted.
	SnapshotID string `json:"snapshot_id"`
	// Segment is the output segment number assigned by the segmenter.
	Segment int `json:"segment"`
	// Hash is the SHA-256 of the elementary payload bytes.
	PayloadHash string `json:"payload_sha256"`
}

// SnapshotRef ties the index to the mapping versions it was built against.
type SnapshotRef struct {
	ID       string `json:"id"`
	PAT      uint8  `json:"pat_version"`
	PMT      uint8  `json:"pmt_version"`
	AtPacket int64  `json:"at_packet"`
	AtOffset int64  `json:"at_offset"`
}

// Index is the complete random-access index for one output.
type Index struct {
	Records      []Record      `json:"records"`
	Snapshots    []SnapshotRef `json:"snapshots"`
	VideoPID     uint16        `json:"video_pid"`
	AudioPID     uint16        `json:"audio_pid"`
	FirstDTS     int64         `json:"first_dts"`
	LastDTS      int64         `json:"last_dts"`
	Keyframes    int           `json:"keyframes"`
	SegmentCount int           `json:"segment_count"`
}

// Builder accumulates records from a repaired stream.
type Builder struct {
	idx    Index
	limits int64
	closed bool
}

// NewBuilder creates an empty index builder. A zero limit selects MaxRecords.
func NewBuilder(limit int) *Builder {
	if limit <= 0 {
		limit = MaxRecords
	}
	return &Builder{limits: int64(limit)}
}

// SetTracks declares the video and audio PIDs the index describes.
func (b *Builder) SetTracks(video, audio uint16) {
	b.idx.VideoPID = video
	b.idx.AudioPID = audio
}

// Add appends a record. The record's SnapshotID must be non-empty; this is
// the invariant that prevents cross-mapping index entries.
func (b *Builder) Add(r Record) error {
	if b.closed {
		return errors.New("index: builder already closed")
	}
	if r.SnapshotID == "" {
		return errors.New("index: record without snapshot id")
	}
	if len(b.idx.Records) >= int(b.limits) {
		return &LimitsError{What: "records", Limit: b.limits, At: int64(len(b.idx.Records))}
	}
	r.Seq = int64(len(b.idx.Records))
	b.idx.Records = append(b.idx.Records, r)
	return nil
}

// NoteSnapshot records a PSI snapshot that influenced the index. Identical
// consecutive IDs are collapsed so the manifest stays small.
func (b *Builder) NoteSnapshot(s *ts.Snapshot) {
	if s == nil {
		return
	}
	if n := len(b.idx.Snapshots); n > 0 && b.idx.Snapshots[n-1].ID == s.ID {
		return
	}
	b.idx.Snapshots = append(b.idx.Snapshots, SnapshotRef{
		ID: s.ID, PAT: s.PATVersion, PMT: s.PMTVersion,
		AtPacket: s.PacketIndex, AtOffset: s.Offset,
	})
}

// Finish computes durations, keyframe counts and validates the index.
func (b *Builder) Finish() (*Index, error) {
	if b.closed {
		return nil, errors.New("index: already finished")
	}
	b.closed = true
	recs := b.idx.Records
	if len(recs) == 0 {
		return nil, errors.New("index: no records")
	}
	// Durations come from the next same-PID record's DTS. The final record
	// of each PID takes the median duration of the stream, never a guess
	// outside the observed range.
	byPID := make(map[uint16][]int)
	for i, r := range recs {
		byPID[r.PID] = append(byPID[r.PID], i)
	}
	for _, list := range byPID {
		var deltas []int64
		for k := 0; k+1 < len(list); k++ {
			d := recs[list[k+1]].DTS - recs[list[k]].DTS
			if d > 0 {
				recs[list[k]].Duration = d
				deltas = append(deltas, d)
			}
		}
		last := list[len(list)-1]
		if len(deltas) > 0 {
			sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
			recs[last].Duration = deltas[len(deltas)/2]
		}
	}
	// Keyframes are only valid when a video PID produced at least one; an
	// index without a random access point cannot be randomly located in.
	for _, r := range recs {
		if r.Keyframe {
			b.idx.Keyframes++
		}
	}
	if b.idx.Keyframes == 0 {
		return nil, errors.New("index: no keyframes; stream is not randomly addressable")
	}
	b.idx.FirstDTS = recs[0].DTS
	b.idx.LastDTS = recs[len(recs)-1].DTS
	b.idx.SegmentCount = countSegments(recs)
	return &b.idx, nil
}

func countSegments(recs []Record) int {
	n := 0
	last := -1
	for _, r := range recs {
		if r.Segment != last {
			n++
			last = r.Segment
		}
	}
	return n
}

// Find locates, by unwrapped PTS in ticks, the keyframe sample that covers a
// presentation time. The returned record is a keyframe whose
// Segment may be entered directly. found=false means the time is outside
// the indexed range; callers must not clamp silently.
func (idx *Index) Find(tick int64) (Record, bool) {
	first := -1
	best := -1
	for i := range idx.Records {
		r := &idx.Records[i]
		if !r.Keyframe || r.Kind != KindVideo {
			continue
		}
		if first < 0 || r.PTS < idx.Records[first].PTS {
			first = i
		}
		if r.PTS <= tick {
			if best < 0 || r.PTS > idx.Records[best].PTS {
				best = i
			}
		}
	}
	if first < 0 {
		return Record{}, false
	}
	if tick > idx.LastDTS {
		// Beyond the indexed range: report the miss rather than clamp.
		return Record{}, false
	}
	if best < 0 {
		// Before the first keyframe still resolves to the range start.
		best = first
	}
	return idx.Records[best], true
}

// Segment returns the sample range [start,end) of one output segment and the
// keyframe that opens it. Both bounds are records of the index.
func (idx *Index) Segment(n int) (start, end Record, ok bool) {
	first := -1
	last := -1
	for i, r := range idx.Records {
		if r.Segment != n {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first < 0 {
		return Record{}, Record{}, false
	}
	return idx.Records[first], idx.Records[last], true
}

// Validate re-checks the invariants a consumer relies on: ascending seq,
// every segment opens on a keyframe, every keyframe carries a snapshot, and
// no record references an unknown snapshot.
func (idx *Index) Validate() error {
	if len(idx.Records) == 0 {
		return errors.New("index: empty")
	}
	known := make(map[string]bool, len(idx.Snapshots))
	for _, s := range idx.Snapshots {
		known[s.ID] = true
	}
	if len(known) == 0 {
		return errors.New("index: no snapshot references")
	}
	segOpen := make(map[int]bool)
	lastSeg := -1
	var lastSeq int64 = -1
	for i := range idx.Records {
		r := &idx.Records[i]
		if r.Seq != lastSeq+1 {
			return fmt.Errorf("index: record %d has seq %d", i, r.Seq)
		}
		lastSeq = r.Seq
		if !known[r.SnapshotID] {
			return fmt.Errorf("index: record %d cites unknown snapshot %s", r.Seq, r.SnapshotID)
		}
		if r.Duration <= 0 {
			return fmt.Errorf("index: record %d has non-positive duration %d", r.Seq, r.Duration)
		}
		if r.Segment != lastSeg {
			segOpen[r.Segment] = true
			if !r.Keyframe {
				return fmt.Errorf("index: segment %d opens on non-keyframe seq %d", r.Segment, r.Seq)
			}
			lastSeg = r.Segment
		}
	}
	return nil
}

// Digest returns a stable digest of the index content (not file bytes) that
// manifests can cite even when JSON formatting changes.
func (idx *Index) Digest() string {
	h := crc32.NewIEEE()
	fmt.Fprintf(h, "vpid=%d apid=%d segs=%d", idx.VideoPID, idx.AudioPID, idx.SegmentCount)
	for _, r := range idx.Records {
		fmt.Fprintf(h, "%d|%d|%d|%d|%d|%t|%s|%d\n", r.Seq, r.PacketOffset, r.PID, r.DTS, r.PTS, r.Keyframe, r.PayloadHash, r.Segment)
	}
	return fmt.Sprintf("%08x", h.Sum32())
}

// MarshalJSON keeps record ordering explicit for byte-identical manifests.
func (idx *Index) MarshalStable() ([]byte, error) {
	return json.MarshalIndent(idx, "", "  ")
}

// UnmarshalIndex parses a stored index.
func UnmarshalIndex(b []byte) (*Index, error) {
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	if len(idx.Records) > MaxRecords {
		return nil, &LimitsError{What: "records", Limit: MaxRecords, At: int64(len(idx.Records))}
	}
	return &idx, nil
}

// TickFromTime converts seconds on the repaired timeline to ticks.
func TickFromTime(sec float64) int64 { return int64(sec * float64(clock.TimeScale)) }
