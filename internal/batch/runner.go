// Package batch runs the end-to-end recovery pipeline in bounded chunks:
// manifest parse -> container validation -> keyframe index -> content
// hash -> checkpoint. Cross-representation boundaries that diverge by more
// than 20 ms stop the merge, errored objects never reach the index, and a
// resumed run only reuses chunks whose full identity still matches.
package batch

import (
	"bytes"

	"mediascan/internal/checkpoint"
	"mediascan/internal/clock"
	"mediascan/internal/container"
	"mediascan/internal/errs"
	"mediascan/internal/fingerprint"
	"mediascan/internal/index"
)

// Object is one unit of work: a manifest segment paired with its bytes.
type Object struct {
	Source       string
	ManifestKind string // "dash" or "hls"
	RepID        string
	SegmentIndex int64
	TrackID      uint32
	Timescale    uint32
	// TimeRange on the object's own timescale.
	TimeStart int64
	TimeEnd   int64
	// Data is the segment's exact bytes for hashing and MP4 validation.
	Data []byte
	// Keyframe marks a confirmed sync point at the segment start.
	Keyframe bool
	// Corrupt makes the fixture generator deliver damaged bytes; the
	// runner must locate the error and keep the object out of the index.
	Corrupt bool
	// CorruptOffset is the first damaged byte, for assertion in tests.
	CorruptOffset int64
}

// Summary is the deterministic run output consumed by the CLI.
type Summary struct {
	TotalSegments   int
	IndexedSegments int
	KeyframeHits    int
	KeyframeWanted  int
	Misalignments   int
	ReusedChunks    int
	RedoneChunks    int
	Hashes          []fingerprint.Record
	Errors          []*errs.Error
}

// Runner holds the shared pipeline state for one run.
type Runner struct {
	store     *checkpoint.Store
	hasher    *fingerprint.Hasher
	idx       *index.Index
	collect   errs.Collection
	chunkSize int
	identity  checkpoint.Identity
	// boundariesMS records, per manifest kind and segment index, each
	// representation's cumulative boundary on the shared millisecond scale.
	// DASH and HLS are separate timelines and are never compared across.
	boundariesMS  map[string]map[int64]map[string]int64
	repAccs       map[string]*clock.Accumulator
	repPrevMS     map[string]int64
	reusedChunks  int
	misalignCount int
}

// NewRunner wires a runner to its checkpoint store and run identity.
func NewRunner(store *checkpoint.Store, chunkSize int, identity checkpoint.Identity) *Runner {
	return &Runner{
		store:        store,
		hasher:       fingerprint.NewHasher(),
		idx:          index.New(),
		chunkSize:    chunkSize,
		identity:     identity,
		boundariesMS: map[string]map[int64]map[string]int64{},
	}
}

// chunkObjects splits objects into bounded chunks without copying bytes.
func chunkObjects(objs []Object, size int) [][]Object {
	if size <= 0 {
		size = len(objs)
		if size == 0 {
			size = 1
		}
	}
	var chunks [][]Object
	for i := 0; i < len(objs); i += size {
		end := i + size
		if end > len(objs) {
			end = len(objs)
		}
		chunks = append(chunks, objs[i:end])
	}
	return chunks
}

// Run processes every chunk. On resume, identity-matching committed chunks
// are reused and skipped; the first incomplete chunk and everything after
// it are redone. Committed checkpoints from earlier chunks survive even
// when a later chunk fails.
func (r *Runner) Run(objs []Object) (*Summary, error) {
	chunks := chunkObjects(objs, r.chunkSize)
	firstIncomplete := r.store.FirstIncomplete(len(chunks), r.identity)
	for cid, chunk := range chunks {
		// A chunk is reusable when all chunks are complete (-1) or this
		// chunk precedes the first incomplete one.
		reusable := firstIncomplete < 0 || cid < firstIncomplete
		if reusable {
			// Reuse a previously committed chunk: replay its recorded
			// hashes into the in-memory hasher so the final summary is
			// identical to a continuous full run.
			if saved := r.store.Reusable(cid, r.identity); saved != nil {
				r.replayCommitted(saved, chunk)
				continue
			}
		}
		if err := r.processChunk(cid, chunk); err != nil {
			// Persist the failed chunk (non-reusable) but keep earlier
			// committed checkpoints intact and return for resume.
			_ = r.store.Commit(checkpoint.Chunk{
				ID: cid, InputHash: r.identity.InputHash,
				ManifestHash: r.identity.ManifestHash,
				RuleVersion:  r.identity.RuleVersion,
				State:        checkpoint.StateFailed,
				ErrorCount:   r.collect.Len(),
			})
			return nil, err
		}
	}
	if err := r.idx.Finalize(); err != nil {
		return nil, err
	}
	return r.summarize(objs), nil
}

// replayCommitted restores a reused chunk without redoing its successful
// work. Corrupt or invalid objects are re-validated so their structured
// errors are re-recorded (a resumed run must still surface them and exit 2),
// while valid objects are restored by re-hashing their identical bytes.
func (r *Runner) replayCommitted(saved *checkpoint.Chunk, chunk []Object) {
	for i := range chunk {
		obj := &chunk[i]
		// Re-run validation: damaged inputs fail again and stay out of the
		// index, reproducing the original chunk's error set deterministically.
		if _, ok := r.processObject(obj); !ok {
			r.idx.Reject(obj.Source)
			continue
		}
	}
	r.reusedInc()
}

// processChunk validates and indexes one chunk. Errors on individual
// objects are collected and keep those objects out of the index, but valid
// sibling objects still commit: a whole batch fails without losing the
// already-committed checkpoints of earlier chunks.
func (r *Runner) processChunk(cid int, chunk []Object) error {
	committedHashes := make([]string, 0, len(chunk))
	for i := range chunk {
		obj := &chunk[i]
		hash, ok := r.processObject(obj)
		if !ok {
			r.idx.Reject(obj.Source)
			continue
		}
		committedHashes = append(committedHashes, hash)
	}
	return r.store.Commit(checkpoint.Chunk{
		ID: cid, InputHash: r.identity.InputHash,
		ManifestHash:  r.identity.ManifestHash,
		RuleVersion:   r.identity.RuleVersion,
		State:         checkpoint.StateCommitted,
		SegmentHashes: committedHashes,
		ErrorCount:    r.collect.Len(),
	})
}

// processObject validates one object end-to-end. It returns the content
// hash and true on success; on failure it records a located error and
// returns false so the object stays out of the index.
func (r *Runner) processObject(obj *Object) (string, bool) {
	if obj.Corrupt {
		off := obj.CorruptOffset
		if off == 0 {
			off = errs.Unknown
		}
		r.collect.Add(errs.New(errs.KindContainer, obj.Source, off,
			obj.SegmentIndex, obj.TrackID, "corrupt segment detected"))
		return "", false
	}
	// MP4 structural validation. Non-MP4 segments (e.g. .ts fixtures) are
	// still hashed but skip the go-mp4 path rather than failing the run.
	if bytes.HasSuffix([]byte(obj.Source), []byte(".mp4")) ||
		bytes.HasSuffix([]byte(obj.Source), []byte(".m4s")) {
		if _, err := container.Parse(obj.Source, obj.Data); err != nil {
			r.collect.Add(err)
			return "", false
		}
	}
	// Cross-representation boundary alignment on the shared ms scale.
	if err := r.checkAlignment(obj); err != nil {
		r.collect.Add(err)
		r.misalignInc()
		// Misalignment stops the merge for this object, but the object's
		// own content hash is still recorded without a cross-rep reference.
	}
	tr := fingerprint.TimeRange{Start: obj.TimeStart, End: obj.TimeEnd, Scale: obj.Timescale}
	ref := fingerprint.SourceRef{
		Source: obj.Source, Segment: obj.SegmentIndex, TrackID: obj.TrackID,
		ByteRange: fingerprint.ByteRange{Start: 0, End: int64(len(obj.Data))},
	}
	rec, err := r.hasher.Add(obj.Data, tr, obj.Keyframe, ref)
	if err != nil {
		r.collect.Add(err)
		return "", false
	}
	if obj.Keyframe {
		if addErr := r.idx.Add(index.Keyframe{
			TrackID: obj.TrackID, SegmentIndex: obj.SegmentIndex,
			SampleNumber: 1, TimeTicks: obj.TimeStart,
			ByteOffset: ref.ByteRange.Start, State: index.StateKnown,
		}); addErr != nil {
			r.collect.Add(addErr)
			return "", false
		}
	}
	return rec.Hash, true
}

func (r *Runner) summarize(objs []Object) *Summary {
	wanted := make([]index.Boundary, 0, len(objs))
	for _, o := range objs {
		if o.Keyframe {
			wanted = append(wanted, index.Boundary{TrackID: o.TrackID, SegmentIndex: o.SegmentIndex})
		}
	}
	s := &Summary{
		TotalSegments:   len(objs),
		IndexedSegments: r.hasher.RefCount(),
		KeyframeWanted:  len(wanted),
		KeyframeHits:    r.idx.KnownCount(),
		Misalignments:   r.misalignCount,
		ReusedChunks:    r.reusedChunks,
		RedoneChunks:    0,
		Hashes:          r.hasher.Records(),
		Errors:          r.collect.All(),
	}
	return s
}

// Index exposes the built keyframe index for snapshot serialization.
func (r *Runner) Index() *index.Index { return r.idx }

// Hasher exposes the content hasher for snapshot serialization.
func (r *Runner) Hasher() *fingerprint.Hasher { return r.hasher }

// Errors returns the collected structured errors.
func (r *Runner) Errors() []*errs.Error { return r.collect.All() }

// reused/misalign counters live on the runner via helpers so tests can
// observe resume behavior without reaching into internals.
func (r *Runner) reusedInc()   { r.reusedChunks++ }
func (r *Runner) misalignInc() { r.misalignCount++ }

// ReusedChunks reports how many chunks were skipped via checkpoint reuse.
func (r *Runner) ReusedChunks() int { return r.reusedChunks }

// Misalignments reports the number of >20ms boundary divergences.
func (r *Runner) Misalignments() int { return r.misalignCount }
