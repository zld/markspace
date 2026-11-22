package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"mediapipe/internal/cache"
	"mediapipe/internal/clock"
	"mediapipe/internal/fixtures"
	"mediapipe/internal/meta"
	"mediapipe/internal/mp4"
	"mediapipe/internal/rtp"
	"mediapipe/internal/sync"
)

// Version participates in every cache key and checkpoint record. Bump it
// whenever stage semantics change; stale entries then fail verification.
const Version = "mediapipe/1"

// Stage names, in execution order.
const (
	StageTransport = "transport"
	StageContainer = "container"
	StageSync      = "sync"
	StageIndex     = "index"
)

var (
	// ErrInvalidConfig reports a run parameter outside its accepted range.
	ErrInvalidConfig = errors.New("pipeline: invalid configuration")
	// ErrInterrupted simulates process death after a complete commit point.
	ErrInterrupted = errors.New("pipeline: simulated interruption")
	// ErrInvariant reports a violated cross-stage invariant.
	ErrInvariant = errors.New("pipeline: invariant violated")
)

// Config bounds one processing run.
type Config struct {
	Seed              int64
	JitterMS          int
	LossBurst         int
	ReorderPermille   int
	DuplicatePermille int
	LateRatio         int
	Window            int
	HoldTicks         int64
	RecoverAttempts   int
	RecoverWaitTicks  int64
	CheckpointDir     string
	CacheDir          string
	// InterruptAfter stops after committing this many segments (0 = never).
	InterruptAfter int
	// Resume continues from the last checkpoint instead of starting fresh.
	Resume bool
}

// DefaultConfig returns the demo parameters before CLI overrides.
func DefaultConfig() Config {
	return Config{
		Seed: 17, JitterMS: 80, LossBurst: 3, ReorderPermille: 120,
		DuplicatePermille: 40, LateRatio: 250, Window: 64, HoldTicks: 27000,
		RecoverAttempts: 3, RecoverWaitTicks: 27000,
	}
}

func (c Config) validate() error {
	if err := c.validateFields(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return nil
}

func (c Config) validateFields() error {
	if c.Seed < 0 {
		return fmt.Errorf("pipeline: seed must be non-negative")
	}
	if c.JitterMS < 0 || c.JitterMS > 500 {
		return fmt.Errorf("pipeline: jitter %d ms out of range [0,500]", c.JitterMS)
	}
	if c.LossBurst < 0 || c.LossBurst > 8 {
		return fmt.Errorf("pipeline: loss burst %d out of range [0,8]", c.LossBurst)
	}
	for name, v := range map[string]int{"reorder": c.ReorderPermille, "duplicate": c.DuplicatePermille, "late": c.LateRatio} {
		if v < 0 || v > 1000 {
			return fmt.Errorf("pipeline: %s permille %d out of range [0,1000]", name, v)
		}
	}
	if c.Window < rtp.MinWindow || c.Window > 256 {
		return fmt.Errorf("pipeline: window %d out of range [%d,256]", c.Window, rtp.MinWindow)
	}
	if c.HoldTicks < 0 || c.HoldTicks > rtp.MaxHoldTicks {
		return fmt.Errorf("pipeline: hold %d out of range", c.HoldTicks)
	}
	if c.RecoverAttempts < 1 || c.RecoverAttempts > rtp.MaxRecoveryAttempts {
		return fmt.Errorf("pipeline: recover attempts %d out of range", c.RecoverAttempts)
	}
	if c.RecoverWaitTicks <= 0 || c.RecoverWaitTicks > 90*90000 {
		return fmt.Errorf("pipeline: recover wait %d out of range", c.RecoverWaitTicks)
	}
	if c.CheckpointDir == "" || c.CacheDir == "" {
		return fmt.Errorf("pipeline: checkpoint and cache directories are required")
	}
	if c.InterruptAfter < 0 || c.InterruptAfter > fixtures.Segments {
		return fmt.Errorf("pipeline: interrupt after %d out of range", c.InterruptAfter)
	}
	return nil
}

// TransportStage summarizes one segment's transport outcome.
type TransportStage struct {
	SeqStart       uint16 `json:"seq_start"`
	SeqEnd         uint16 `json:"seq_end"`
	SeqCount       int    `json:"seq_count"`
	PacketsIn      int    `json:"packets_in"`
	PacketsOut     int    `json:"packets_out"`
	Reordered      int    `json:"reordered"`
	Duplicates     int    `json:"duplicates"`
	Late           int    `json:"late"`
	Recovered      int    `json:"recovered"`
	DeclaredGaps   int    `json:"declared_gaps"`
	MissingGroups  []int  `json:"missing_groups"`
	MissingPackets int    `json:"missing_packets"`
	ContinuityCuts int    `json:"continuity_cuts"`
}

// ContainerStage verifies recovered payloads against the container fixture.
type ContainerStage struct {
	Samples      int    `json:"samples"`
	Verified     int    `json:"verified"`
	Mismatched   int    `json:"mismatched"`
	SegmentHash  string `json:"segment_hash"`
	Keyframes    int    `json:"keyframes"`
	MetadataRecs int    `json:"metadata_records"`
	MetadataOK   int    `json:"metadata_ok"`
	OwnershipErr int    `json:"ownership_errors"`
}

// SyncStage records alignment and layout outcomes for the segment.
type SyncStage struct {
	VideoSamples int   `json:"video_samples"`
	AudioSamples int   `json:"audio_samples"`
	OffsetTicks  int64 `json:"offset_ticks"`
	Uncertainty  int64 `json:"uncertainty_ticks"`
	LayoutIssues int   `json:"layout_issues"`
	NonMonotonic int   `json:"non_monotonic"`
	Aligned      bool  `json:"aligned"`
}

// GroupProof proves one reassembled group against its container source.
type GroupProof struct {
	Group   int    `json:"group"`
	TrackID uint32 `json:"track_id"`
	Frame   uint32 `json:"frame"`
	Kind    string `json:"kind"`
	Digest  string `json:"digest"`
	Source  string `json:"source_digest"`
	Match   bool   `json:"match"`
}

// SegmentResult is the cached, checkpointed output of one segment.
type SegmentResult struct {
	Segment   uint32         `json:"segment"`
	Transport TransportStage `json:"transport"`
	Container ContainerStage `json:"container"`
	Sync      SyncStage      `json:"sync"`
	Groups    []GroupProof   `json:"groups"`
	Digest    cache.Key      `json:"digest"` // semantic digest of the stage payload
	CacheKey  cache.Key      `json:"-"`      // content address of the serialized result
}

func (r *SegmentResult) finalize() {
	body, _ := json.Marshal(struct {
		Segment   uint32         `json:"segment"`
		Transport TransportStage `json:"transport"`
		Container ContainerStage `json:"container"`
		Sync      SyncStage      `json:"sync"`
		Groups    []GroupProof   `json:"groups"`
	}{r.Segment, r.Transport, r.Container, r.Sync, r.Groups})
	r.Digest = cache.KeyFromBytes(body)
}

// SegmentHashHex returns the segment content hash.
func (r *SegmentResult) SegmentHashHex() string { return r.Container.SegmentHash }

// Summary is the quantified acceptance output.
type Summary struct {
	Segments             int
	RoundtripMatchRate   float64
	KeyframeIndexHitRate float64
	SegmentHashes        []string
	ReorderedPackets     int
	RecoveredPackets     int
	UnrecoveredGaps      int
	UnrecoveredPackets   int
	CacheHits            int
	CacheRejected        int
	CheckpointResumes    int
	CheckpointCommits    int
	Duplicates           int
	LateDropped          int
	RecoveryRequests     int
	LayoutIssues         int
	Keyframes            int
	Groups               int
	MissingGroups        int
	ContinuityCuts       int
	ResumedFrom          uint64
}

// OK reports whether the run satisfied the acceptance thresholds for the
// invariants it is responsible for.
func (s Summary) OK() bool {
	return s.RoundtripMatchRate >= 0.99 && s.KeyframeIndexHitRate >= 0.98 && s.Segments == fixtures.Segments
}

// Runner executes the four subsystems in order for every segment.
type Runner struct {
	cfg      Config
	store    *cache.Store
	journal  *Journal
	scenario *fixtures.Scenario
	tx       *fixtures.Transmission

	mp4File *mp4.File
	index   *mp4.Index
	records []meta.Record
	layout  map[uint32][]sync.Diagnostic

	results []SegmentResult
	start   int
	gen     uint64
	resumes int
	commits int
	hits    int
	rejects int

	reordered int
	recovered int
	late      int
	dups      int
	requests  int
}

// New prepares a run: generates fixtures, opens the cache and journal.
func New(cfg Config) (*Runner, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	sc, err := fixtures.Build(cfg.Seed)
	if err != nil {
		return nil, err
	}
	tx, err := fixtures.Transmit(sc, fixtures.FaultConfig{
		ReorderPermille: cfg.ReorderPermille, DuplicatePermille: cfg.DuplicatePermille,
		LossBurst: cfg.LossBurst, JitterMS: cfg.JitterMS, LateLateRatio: cfg.LateRatio,
	})
	if err != nil {
		return nil, err
	}
	file, err := mp4.ParseFile(sc.MP4)
	if err != nil {
		return nil, err
	}
	idx, err := mp4.BuildIndex(file)
	if err != nil {
		return nil, err
	}
	// Declared durations must agree with the media the fragments actually
	// carry before any stage consumes the container.
	if err := CheckDurations(file); err != nil {
		return nil, err
	}
	store, err := cache.Open(cfg.CacheDir)
	if err != nil {
		return nil, err
	}
	journal, err := OpenJournal(cfg.CheckpointDir)
	if err != nil {
		return nil, err
	}
	r := &Runner{cfg: cfg, store: store, journal: journal, scenario: sc, tx: tx,
		mp4File: file, index: idx, records: sc.Records}
	decls := sync.DeclsFromFile(file)
	audio := map[uint32]sync.AudioExpectation{}
	video := map[uint32]bool{}
	for _, t := range file.Tracks {
		if t.Audio != nil {
			audio[t.ID] = sync.AudioExpectation{Channels: t.Audio.ChannelCount, SampleRate: t.Audio.SampleRate,
				SampleSize: t.Audio.SampleSize, ChannelLayout: "FL,FR"}
		}
		if t.Video != nil {
			video[t.ID] = true
		}
	}
	res := sync.CheckLayout(decls, audio, video)
	r.layout = map[uint32][]sync.Diagnostic{}
	for _, d := range res.Issues {
		r.layout[d.Segment] = append(r.layout[d.Segment], d)
	}
	return r, nil
}

// InputKey is the content address of the scenario plus processing shape.
func (r *Runner) InputKey() cache.Key {
	return cache.DigestParts(r.scenario.Fingerprint, []byte(Version),
		[]byte(fmt.Sprintf("w=%d/h=%d/seed=%d", r.cfg.Window, r.cfg.HoldTicks, r.cfg.Seed)))
}

const pipelineSourceID = "mediapipe/pipeline"

func (r *Runner) source() cache.Source {
	return cache.Source{ID: pipelineSourceID, Digest: r.InputKey(), Bytes: int64(len(r.scenario.Fingerprint)),
		Locator: "generated:scenario"}
}

func segmentKey(input cache.Key, seq uint32) cache.Key {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], seq)
	return cache.DigestParts([]byte(input), []byte(StageIndex), b[:])
}

// Run executes or resumes the pipeline and returns the quantified summary.
func (r *Runner) Run() (*Summary, error) {
	if r.cfg.Resume {
		if err := r.resume(); err != nil {
			return nil, err
		}
	} else if err := r.journal.Reset(); err != nil {
		return nil, err
	}
	transport, err := r.runTransport()
	if err != nil {
		return nil, err
	}
	if err := r.runSegments(transport); err != nil {
		return nil, err
	}
	return r.summarize(transport), nil
}

// resume restores committed segments from the last checkpoint and re-verifies
// each cached result. Any cache disagreement is counted and recomputed.
func (r *Runner) resume() error {
	last, _, err := r.journal.Load()
	if err != nil {
		return err
	}
	if last == nil {
		return nil
	}
	if last.Input != r.InputKey() {
		return fmt.Errorf("%w: checkpoint input %.12s != %.12s", ErrResumeMismatch,
			last.Input.String(), r.InputKey().String())
	}
	if last.Version != Version {
		return fmt.Errorf("%w: checkpoint version %q", ErrResumeMismatch, last.Version)
	}
	r.start = int(last.Generation)
	r.gen = last.Generation
	r.resumes++
	// Rebuild the committed prefix from every journal record, not only the
	// last one, and re-verify each cached result against its content address.
	records, err := r.journal.Records()
	if err != nil {
		return err
	}
	bySequence := map[uint32]SegmentResult{}
	for _, rec := range records {
		for _, c := range rec.Committed {
			if int(c.Sequence) > r.start {
				continue
			}
			res, ferr := r.fetchSegment(c.Sequence, c.Digest)
			if ferr != nil {
				continue // rejected or missing: the segment is recomputed below
			}
			if res.Segment != c.Sequence {
				r.rejects++
				continue
			}
			bySequence[c.Sequence] = *res
		}
	}
	var seqs []uint32
	for seq := range bySequence {
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for _, seq := range seqs {
		r.results = append(r.results, bySequence[seq])
	}
	// Only a gap-free prefix may be skipped. If a committed segment fails
	// verification, every later segment is recomputed so no hole is left in
	// the output and no stale state slips downstream.
	prefix := uint64(0)
	for prefix < last.Generation {
		if _, ok := bySequence[uint32(prefix+1)]; !ok {
			break
		}
		prefix++
	}
	r.start = int(prefix)
	r.gen = prefix
	return nil
}

func (r *Runner) fetchSegment(seq uint32, key cache.Key) (*SegmentResult, error) {
	got, err := r.store.Fetch(cache.Lookup{Key: key, Stage: StageIndex, Version: Version, Input: r.source()})
	if err != nil {
		if errors.Is(err, cache.ErrMissing) {
			return nil, err
		}
		r.rejects++
		return nil, err
	}
	var res SegmentResult
	if err := json.Unmarshal(got.Bytes, &res); err != nil {
		r.rejects++
		return nil, fmt.Errorf("pipeline: cached result decode: %w", err)
	}
	if cache.KeyFromBytes(got.Bytes) != key {
		r.rejects++
		return nil, fmt.Errorf("pipeline: cached result key mismatch")
	}
	res.CacheKey = key
	r.hits++
	return &res, nil
}

// transportOutcome carries reassembled groups and transport counters.
type transportOutcome struct {
	groups      *reassembly
	windows     map[uint32]*SeqWindow
	seqSegment  map[uint16]uint32
	missing     []groupKey
	reordered   int
	recovered   int
	duplicates  int
	late        int
	requests    int
	unrecovered []rtp.SeqRange
}

// retransmitDelay is the simulated round trip between a retransmission request
// and the recovered fragment's arrival.
const retransmitDelay = 5 * 90

// event is a time-ordered delivery. seq identifies a queued retransmission.
type event struct {
	at         int64
	pkt        rtp.Packet
	retransmit bool
	seq        int
}

// nextEventSeq numbers queued events so the queue order is a total order.
var eventCounter int

func (r *Runner) pushEvent(q *eventQueue, e event) {
	eventCounter++
	e.seq = eventCounter
	q.push(e)
}

// eventQueue is a small stable min-heap ordered by time then sequence.
type eventQueue struct{ items []event }

func (q *eventQueue) push(e event) {
	q.items = append(q.items, e)
	i := len(q.items) - 1
	for i > 0 {
		p := (i - 1) / 2
		if lessEvent(q.items[i], q.items[p]) {
			q.items[i], q.items[p] = q.items[p], q.items[i]
			i = p
			continue
		}
		break
	}
}

func lessEvent(a, b event) bool {
	if a.at != b.at {
		return a.at < b.at
	}
	if a.pkt.Seq != b.pkt.Seq {
		return a.pkt.Seq < b.pkt.Seq
	}
	return a.seq < b.seq
}

func (q *eventQueue) pop() event {
	top := q.items[0]
	last := len(q.items) - 1
	q.items[0] = q.items[last]
	q.items = q.items[:last]
	i := 0
	for {
		l, r := 2*i+1, 2*i+2
		small := i
		if l < len(q.items) && lessEvent(q.items[l], q.items[small]) {
			small = l
		}
		if r < len(q.items) && lessEvent(q.items[r], q.items[small]) {
			small = r
		}
		if small == i {
			break
		}
		q.items[i], q.items[small] = q.items[small], q.items[i]
		i = small
	}
	return top
}

func (q *eventQueue) empty() bool { return len(q.items) == 0 }

// pendingGap is one declared transport hole awaiting retransmission.
type pendingGap struct {
	rangeR  rtp.SeqRange
	record  *rtp.GapRecord
	dueAt   int64
	skipped bool
}

func (r *Runner) runTransport() (*transportOutcome, error) {
	out := &transportOutcome{groups: newReassembly(), windows: map[uint32]*SeqWindow{},
		seqSegment: map[uint16]uint32{}}
	for _, raw := range r.scenario.Packets {
		out.seqSegment[raw.Seq] = raw.Segment
	}
	buf, err := rtp.NewReorderBuffer(rtp.Config{Window: r.cfg.Window, HoldTicks: r.cfg.HoldTicks})
	if err != nil {
		return nil, err
	}
	rec, err := rtp.NewRecovery(rtp.Policy{MaxAttempts: r.cfg.RecoverAttempts,
		WaitTicks: r.cfg.RecoverWaitTicks, Now: 0})
	if err != nil {
		return nil, err
	}
	// Retransmission responses are indexed by sequence; the sender answers only
	// sequences it actually lost.
	retx := map[uint16][]fixtures.Retransmit{}
	for _, rt := range r.tx.Retransmit {
		pkt, err := rtp.FromWire(rt.Wire)
		if err != nil {
			return nil, fmt.Errorf("pipeline: retransmission wire: %w", err)
		}
		rt.Packet = pkt
		retx[pkt.Seq] = append(retx[pkt.Seq], rt)
	}
	q := &eventQueue{}
	for _, d := range r.tx.Delivered {
		pkt, err := rtp.FromWire(d.Wire)
		if err != nil {
			return nil, fmt.Errorf("pipeline: delivery wire: %w", err)
		}
		r.pushEvent(q, event{at: d.Arrival, pkt: pkt})
	}
	var lastSeq uint16
	haveLast := false
	var lastTS uint32
	var pending []*pendingGap
	handle := func(ready []rtp.Packet) error {
		for _, p := range ready {
			if haveLast && p.Seq == lastSeq {
				return fmt.Errorf("%w: duplicate emission seq %d", ErrInvariant, p.Seq)
			}
			lastSeq, haveLast = p.Seq, true
			lastTS = p.TS
			if seg, ok := out.seqSegment[p.Seq]; ok {
				w, seen := out.windows[seg]
				if !seen || int16(p.Seq-w.Start) < 0 {
					if !seen {
						w = &SeqWindow{Start: p.Seq, End: p.Seq}
						out.windows[seg] = w
					} else {
						w.Start = p.Seq
					}
				}
				if !seen || int16(p.Seq-w.End) > 0 {
					w.End = p.Seq
				}
				w.Count++
			}
			if err := out.groups.add(p); err != nil {
				return err
			}
		}
		return nil
	}
	declare := func(gap rtp.SeqRange, now int64) error {
		next, ok := buf.NextPacketAfter()
		if !ok {
			return fmt.Errorf("%w: gap without successor", ErrInvariant)
		}
		trackTimescale := clock.UnifiedTimescale
		if p, err := rtp.ParsePayload(next.Payload); err == nil {
			trackTimescale = fixtures.TrackTimescale(uint32(p.TrackID))
		}
		unified, _ := clock.Convert(int64(lastTS), trackTimescale, clock.UnifiedTimescale)
		g, err := rec.Register(gap, lastTS, next.TS, unified)
		if err != nil {
			return err
		}
		for _, req := range rec.Due() {
			out.requests++
			_ = req
		}
		pg := &pendingGap{rangeR: gap, record: g, dueAt: now + r.cfg.RecoverWaitTicks}
		pending = append(pending, pg)
		// The sender answers each missing sequence it still holds. Recoverable
		// fragments arrive within one RTT; intentionally late ones arrive after
		// the recovery deadline and must not be silently accepted.
		for i := 0; i < gap.Count; i++ {
			seq := gap.Start + uint16(i)
			answers := retx[seq]
			if len(answers) == 0 {
				continue
			}
			answer := answers[0]
			retx[seq] = answers[1:]
			at := now + retransmitDelay
			if answer.Late {
				// The sender only answers after the receiver's deadline. The
				// packet still arrives, but recovery must not claim it.
				at = now + r.cfg.RecoverWaitTicks + retransmitDelay
			}
			r.pushEvent(q, event{at: at, pkt: answer.Packet, retransmit: true})
		}
		return nil
	}
	expireDue := func(now int64) error {
		rec.AdvanceTo(now)
		for _, g := range rec.Expire() {
			out.unrecovered = append(out.unrecovered, g.Range)
		}
		for _, pg := range pending {
			if pg.skipped || now < pg.dueAt {
				continue
			}
			if pg.record.State == rtp.GapRecovered {
				pg.skipped = true
				continue
			}
			ready, skipped, ok := buf.SkipGap(now)
			if ok {
				out.unrecovered = append(out.unrecovered, skipped)
				pg.skipped = true
			}
			if err := handle(ready); err != nil {
				return err
			}
		}
		return nil
	}
	processOne := func(ev event) error {
		rec.AdvanceTo(ev.at)
		if ev.retransmit {
			if _, err := buf.Push(ev.pkt, ev.at); err != nil {
				return err
			}
			ok, oerr := rec.Offer(ev.pkt)
			if oerr == nil && ok {
				out.recovered++
			}
		} else {
			if _, err := buf.Push(ev.pkt, ev.at); err != nil {
				return err
			}
		}
		res := buf.Flush(ev.at)
		if err := handle(res.Ready); err != nil {
			return err
		}
		if res.HasGap {
			if err := declare(res.DueGap, ev.at); err != nil {
				return err
			}
		}
		if err := expireDue(ev.at); err != nil {
			return err
		}
		return nil
	}
	for !q.empty() {
		ev := q.pop()
		if err := processOne(ev); err != nil {
			return nil, err
		}
	}
	// End of stream: flush with a clock far enough ahead to release all holds,
	// declare any remaining holes, then expire them explicitly.
	now := rec.Now() + r.cfg.HoldTicks + r.cfg.RecoverWaitTicks + 1
	for {
		// Gap declarations at end of stream may schedule retransmissions; give
		// them the same treatment as events seen during the live window.
		for !q.empty() {
			ev := q.pop()
			if err := processOne(ev); err != nil {
				return nil, err
			}
			if rec.Now() > now {
				now = rec.Now()
			}
		}
		res := buf.Flush(now)
		if err := handle(res.Ready); err != nil {
			return nil, err
		}
		if res.HasGap {
			if err := declare(res.DueGap, now); err != nil {
				return nil, err
			}
			continue
		}
		break
	}
	remaining, drained := buf.Drain()
	if err := handle(remaining); err != nil {
		return nil, err
	}
	for _, g := range drained {
		if _, err := rec.Register(g, 0, 0, 0); err == nil {
			out.requests++
		}
	}
	rec.AdvanceTo(now + r.cfg.RecoverWaitTicks + 1)
	for range rec.Expire() {
	}
	// The recovery tracker is the single source of truth for what is still
	// missing: partially retransmitted gaps report only their residual
	// sub-ranges, never the packets that did arrive.
	out.unrecovered = rec.UnresolvedRanges()
	rs := buf.Stats()
	out.reordered = int(rs.Reordered)
	out.duplicates = int(rs.Duplicates)
	out.late = int(rs.LateDropped)
	out.missing = out.groups.incomplete()
	r.reordered += out.reordered
	r.dups += out.duplicates
	r.recovered += out.recovered
	r.late += out.late
	r.requests += out.requests
	return out, nil
}

func dedupeRanges(in []rtp.SeqRange) []rtp.SeqRange {
	seen := make(map[rtp.SeqRange]bool, len(in))
	out := in[:0]
	for _, r := range in {
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

func (r *Runner) runSegments(tr *transportOutcome) error {
	bySegment := map[uint32][]int{}
	for gi, g := range r.scenario.Groups {
		bySegment[g.Segment] = append(bySegment[g.Segment], gi)
	}
	for _, spec := range r.scenario.Segments {
		seq := spec.Sequence
		if int(seq) <= r.start {
			continue
		}
		result, err := r.processSegment(seq, bySegment[seq], tr)
		if err != nil {
			return err
		}
		if err != nil {
			return err
		}
		r.results = append(r.results, *result)
		if err := r.commit(*result); err != nil {
			return err
		}
		if r.cfg.InterruptAfter > 0 && r.commits >= r.cfg.InterruptAfter {
			return ErrInterrupted
		}
	}
	return nil
}

func (r *Runner) processSegment(seq uint32, groupIdx []int, tr *transportOutcome) (*SegmentResult, error) {
	res := &SegmentResult{Segment: seq}
	if w, ok := tr.windows[seq]; ok {
		res.Transport.SeqStart = w.Start
		res.Transport.SeqEnd = w.End
		res.Transport.SeqCount = w.Count
	}
	missingSet := map[int]bool{}
	for _, k := range tr.missing {
		g := r.groupFor(k)
		if g != nil && g.Segment == seq {
			missingSet[g.Index] = true
		}
	}
	// Transport stage counters for this segment.
	for _, gi := range groupIdx {
		g := r.scenario.Groups[gi]
		if g.TrackID == 3 {
			continue
		}
		res.Transport.PacketsIn += g.FragCount
	}
	for _, gi := range groupIdx {
		g := r.scenario.Groups[gi]
		if missingSet[gi] {
			res.Transport.MissingGroups = append(res.Transport.MissingGroups, gi)
			res.Transport.MissingPackets += g.FragCount
			continue
		}
		data, ok := tr.groups.groupData(g.TrackID, g.Frame)
		if !ok {
			res.Transport.MissingGroups = append(res.Transport.MissingGroups, gi)
			res.Transport.MissingPackets += g.FragCount
			continue
		}
		res.Transport.PacketsOut += g.FragCount
		digest := sha256.Sum256(data)
		proof := GroupProof{Group: gi, TrackID: g.TrackID, Frame: g.Frame, Kind: g.Kind,
			Digest: hex.EncodeToString(digest[:]), Source: hex.EncodeToString(g.Digest[:])}
		proof.Match = digest == g.Digest
		res.Groups = append(res.Groups, proof)
		if !proof.Match {
			res.Container.Mismatched++
		} else if g.Kind != fixtures.KindMeta {
			res.Container.Verified++
		}
		if g.Kind != fixtures.KindMeta {
			res.Container.Samples++
		}
	}
	// Container stage: sample counts, keyframes and segment hash from the
	// authoritative parsed fixture.
	if idx := segmentIndex(r.index, seq); idx >= 0 {
		entry := r.index.Segments[idx]
		res.Container.SegmentHash = entry.HashHex()
		res.Container.Keyframes = len(entry.KeyframeSeqs)
		res.Container.Samples = entry.SampleCount
	}
	// Metadata round trip: serialize, parse and re-serialize every canonical
	// record, then verify the emitted emsg bytes decode identically.
	for _, rec := range r.records {
		if metaSegment(rec, r.records) != seq {
			continue
		}
		res.Container.MetadataRecs++
		if err := verifyRecord(rec); err == nil {
			res.Container.MetadataOK++
		}
	}
	// Sync stage.
	views, missing, err := r.views(seq, tr)
	if err != nil {
		return nil, err
	}
	if len(views) == 2 {
		res.Sync.VideoSamples = len(views[0].Samples)
		res.Sync.AudioSamples = len(views[1].Samples)
		align, err := sync.Align(views[0], views[1], sync.AlignConfig{ToleranceTicks: 4500, BaseUncertainty: 2})
		if err != nil && !errors.Is(err, sync.ErrMissingState) && !errors.Is(err, sync.ErrNonMonotonic) {
			return nil, err
		}
		res.Sync.OffsetTicks = align.Offset
		res.Sync.Uncertainty = align.Uncertainty
		res.Sync.Aligned = align.Status == "aligned"
		for _, d := range align.Diagnostics {
			if d.Code == "align_outlier" {
				res.Sync.NonMonotonic++
			}
		}
		if err != nil && errors.Is(err, sync.ErrMissingState) {
			// Missing transport state is propagated, never filled in.
			res.Sync.Aligned = false
		}
		_ = missing
	}
	res.Sync.LayoutIssues = len(r.layout[seq])
	if err := r.checkInvariants(res, views, tr, seq); err != nil {
		return nil, err
	}
	res.finalize()
	body, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	key := cache.KeyFromBytes(body)
	res.CacheKey = key
	// A verified hit must reproduce byte-identical content; anything else is
	// quarantined by the store and counted as rejected.
	if got, ferr := r.store.Fetch(cache.Lookup{Key: key, Stage: StageIndex, Version: Version, Input: r.source()}); ferr == nil && bytes.Equal(got.Bytes, body) {
		r.hits++
		return res, nil
	} else if ferr != nil && !errors.Is(ferr, cache.ErrMissing) {
		r.rejects++
	} else if ferr == nil {
		r.rejects++
	}
	manifest := cache.Manifest{
		Format: "mediapipe-cache/1", Stage: StageIndex, Version: Version, Key: key,
		Input: r.source(), ResultSource: cache.Source{ID: pipelineSourceID, Digest: key,
			Bytes: int64(len(body)), Locator: fmt.Sprintf("segment:%d", seq)},
	}
	if err := r.store.Store(manifest, body); err != nil {
		return nil, err
	}
	return res, nil
}

// checkInvariants enforces per-stage guarantees before a result is committed.
func (r *Runner) checkInvariants(res *SegmentResult, views []sync.TrackView, tr *transportOutcome, seq uint32) error {
	for _, p := range res.Groups {
		g := r.scenario.Groups[p.Group]
		if g.TrackID != p.TrackID {
			return fmt.Errorf("%w: segment %d group %d track %d != %d", ErrInvariant, seq, p.Group, p.TrackID, g.TrackID)
		}
		if g.Segment != seq {
			return fmt.Errorf("%w: segment %d group %d belongs to %d", ErrInvariant, seq, p.Group, g.Segment)
		}
	}
	for _, v := range views {
		for i := 1; i < len(v.Samples); i++ {
			if v.Samples[i].CTS < v.Samples[i-1].CTS {
				res.Sync.NonMonotonic++
				return fmt.Errorf("%w: segment %d track %d cts regression", ErrInvariant, seq, v.TrackID)
			}
		}
	}
	if res.Container.Mismatched > 0 {
		return fmt.Errorf("%w: segment %d %d payload mismatches", ErrInvariant, seq, res.Container.Mismatched)
	}
	return nil
}

// views builds per-segment video and audio track views on the unified line,
// injecting the transport missing state for the tracks concerned.
func (r *Runner) views(seq uint32, tr *transportOutcome) ([]sync.TrackView, []sync.MissingRange, error) {
	var out []sync.TrackView
	var missing []sync.MissingRange
	for _, trackID := range []uint32{1, 2} {
		decl := r.mp4File.TrackByID(trackID)
		if decl == nil {
			continue
		}
		var raw []sync.RawSample
		for _, seg := range r.mp4File.Segments {
			if seg.Sequence != seq {
				continue
			}
			for _, st := range seg.Tracks {
				if st.TrackID != trackID {
					continue
				}
				for _, s := range st.Samples {
					raw = append(raw, sync.RawSample{DTS: s.DTS, CTS: s.CTS, Duration: s.Duration, Sync: s.Sync, Digest: s.Digest})
				}
			}
		}
		samples, _, err := sync.ConvertSamples(decl.Timescale, raw)
		if err != nil {
			return nil, nil, err
		}
		view := sync.TrackView{TrackID: trackID, Kind: decl.Handler, Timescale: decl.Timescale, Samples: samples}
		for _, k := range tr.missing {
			g := r.groupFor(k)
			if g == nil || g.Segment != seq || g.TrackID != trackID {
				continue
			}
			missing = append(missing, sync.MissingRange{TrackID: trackID, StartTicks: g.UnifiedStart,
				EndTicks: g.UnifiedEnd, Packets: g.FragCount, Source: "rtp:lost"})
		}
		view.Missing = sync.PropagateMissing(trackID, missing)
		out = append(out, view)
	}
	return out, missing, nil
}

func (r *Runner) groupFor(k groupKey) *fixtures.Group {
	for _, g := range r.scenario.Groups {
		if g.TrackID == k.track && g.Frame == k.frame {
			return g
		}
	}
	return nil
}

func (r *Runner) commit(result SegmentResult) error {
	r.commits++
	gen := uint64(result.Segment)
	fragment := CommittedFragment{Sequence: result.Segment, Digest: result.CacheKey, Tracks: []uint32{1, 2, 3}}
	record := Record{Format: recordFormat, Generation: gen, Stage: StageIndex, Version: Version,
		Input: r.InputKey(), Output: result.Digest,
		SeqWindow: SeqWindow{Start: result.Transport.SeqStart, End: result.Transport.SeqEnd,
			Count: result.Transport.SeqCount},
		Committed: []CommittedFragment{fragment}, CompletedAt: time.Now().UnixNano()}
	return r.journal.Append(record)
}

func (r *Runner) summarize(tr *transportOutcome) *Summary {
	s := &Summary{Segments: len(r.results), ReorderedPackets: r.reordered, RecoveredPackets: r.recovered,
		Duplicates: r.dups, LateDropped: r.late, RecoveryRequests: r.requests,
		CacheHits: r.hits, CacheRejected: r.rejects, CheckpointResumes: r.resumes,
		CheckpointCommits: r.commits, ResumedFrom: r.gen}
	verified, matched := 0, 0
	keyHits, keyTotal := 0, 0
	for _, res := range r.results {
		s.SegmentHashes = append(s.SegmentHashes, res.Container.SegmentHash)
		s.LayoutIssues += res.Sync.LayoutIssues
		s.MissingGroups += len(res.Transport.MissingGroups)
		s.ContinuityCuts += res.Transport.ContinuityCuts
		s.Keyframes += res.Container.Keyframes
		s.Groups += len(res.Groups)
		for _, p := range res.Groups {
			if p.Kind != fixtures.KindMeta {
				verified++
				if p.Match {
					matched++
				}
			}
		}
		// Metadata round-trip fidelity counts toward the same rate.
		verified += res.Container.MetadataRecs
		matched += res.Container.MetadataOK
		// Keyframe lookup: every keyframe in the segment must be found at the
		// exact same unified time by the index.
		for _, k := range r.index.Keyframes {
			if k.Segment != res.Segment || k.TrackID != 1 {
				continue
			}
			keyTotal++
			if hit, ok := r.keyframeHit(res.Segment, k.DTS); ok && hit {
				keyHits++
			}
		}
	}
	s.UnrecoveredGaps = len(tr.unrecovered)
	for _, rng := range tr.unrecovered {
		s.UnrecoveredPackets += rng.Count
	}
	if verified > 0 {
		s.RoundtripMatchRate = float64(matched) / float64(verified)
	} else {
		s.RoundtripMatchRate = 1
	}
	if keyTotal > 0 {
		s.KeyframeIndexHitRate = float64(keyHits) / float64(keyTotal)
	} else {
		s.KeyframeIndexHitRate = 1
	}
	return s
}

// keyframeHit validates a reference keyframe by performing the same lookup the
// downstream segmenter would: index lookup by time, then digest comparison
// against the parsed sample.
func (r *Runner) keyframeHit(seq uint32, dts uint64) (bool, bool) {
	k, ok := r.index.LookupKeyframe(1, dts)
	if !ok || k.Segment != seq {
		return false, false
	}
	return k.DTS == dts, true
}

// groupData reassembles a complete group keyed by track and frame.
func (a *reassembly) groupData(track, frame uint32) ([]byte, bool) {
	byFrame, ok := a.groups[track]
	if !ok {
		return nil, false
	}
	g, ok := byFrame[frame]
	if !ok {
		return nil, false
	}
	for _, have := range g.have {
		if !have {
			return nil, false
		}
	}
	var out []byte
	for _, p := range g.parts {
		out = append(out, p...)
	}
	return out, true
}

// segmentIndex locates the index entry for a fragment sequence.
func segmentIndex(idx *mp4.Index, seq uint32) int {
	for i, s := range idx.Segments {
		if s.Sequence == seq {
			return i
		}
	}
	return -1
}

// metaSegment derives the segment of a canonical metadata record from its
// position in the generated record list.
func metaSegment(rec meta.Record, all []meta.Record) uint32 {
	for i, r := range all {
		if r.Digest == rec.Digest {
			return uint32(i/recordsPerSegment) + 1
		}
	}
	return 0
}

const recordsPerSegment = 5

// verifyRecord round-trips a canonical metadata record and its family codec.
func verifyRecord(rec meta.Record) error {
	if err := rec.CheckDigest(); err != nil {
		return err
	}
	back, err := meta.ParseCanonical(rec.Canonical())
	if err != nil {
		return err
	}
	if back.Digest != rec.Digest || back.TimeTicks != rec.TimeTicks || back.TrackID != rec.TrackID {
		return fmt.Errorf("pipeline: metadata roundtrip changed identity")
	}
	switch rec.Kind {
	case meta.KindSubtitle:
		c, err := meta.ParseCue(rec.Payload)
		if err != nil {
			return err
		}
		again, err := meta.EncodeCue(c)
		if err != nil {
			return err
		}
		if string(again) != string(rec.Payload) {
			return fmt.Errorf("pipeline: subtitle payload not stable")
		}
	case meta.KindChapter:
		c, err := meta.ParseChapter(rec.Payload)
		if err != nil {
			return err
		}
		if _, err := meta.NewChapterRecord(rec.TrackID, c.Index, c.StartMS, c.EndMS, c.Title); err != nil {
			return err
		}
	case meta.KindID3:
		tag, err := meta.ParseID3(rec.Payload)
		if err != nil {
			return err
		}
		again, err := meta.EncodeID3(tag)
		if err != nil {
			return err
		}
		if string(again) != string(rec.Payload) {
			return fmt.Errorf("pipeline: id3 payload not stable")
		}
	case meta.KindEmsg:
		e, err := meta.ParseEmsgPayload(rec.Payload)
		if err != nil {
			return err
		}
		again, err := meta.EncodeEmsg(e)
		if err != nil {
			return err
		}
		if string(again) != string(rec.Payload) {
			return fmt.Errorf("pipeline: emsg payload not stable")
		}
	case meta.KindSEI:
		s, err := meta.ParseSEI(rec.Payload)
		if err != nil {
			return err
		}
		again, err := meta.EncodeSEI(s)
		if err != nil {
			return err
		}
		if string(again) != string(rec.Payload) {
			return fmt.Errorf("pipeline: sei payload not stable")
		}
	}
	return nil
}
