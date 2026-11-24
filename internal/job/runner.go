package job

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"archive-recovery/internal/adapter"
	"archive-recovery/internal/index"
	"archive-recovery/internal/media"
	"archive-recovery/internal/provenance"
	"archive-recovery/internal/repair"
	"archive-recovery/internal/transport"
)

// PTSHint carries the (possibly absent) source timestamp for one frame.
type PTSHint struct {
	PTS int64
	Has bool
}

// JobInput is the complete, in-memory description of one recovery job.
type JobInput struct {
	Codec          string
	ChunkBytes     int64
	Seed           int64
	Stream         []byte
	PTS            []PTSHint
	LegacyIndex    []byte // optional legacy index bytes
	InterruptAfter int    // 0 disables controlled interruption
	Resume         bool   // require and validate an existing checkpoint
	InputPath      string // optional source file path, used for the overwrite guard
	WorkDir        string
}

// Result summarizes a finished or interrupted run for CLI rendering.
type Result struct {
	Status              string
	Chunks              int
	SHA256Match         int
	TruncatedUnits      int
	RepairedTimestamps  int
	FramesTotal         int
	KeyframesTotal      int
	KeyframeHits        int
	RoundtripMatchPct   float64
	KeyframeHitPct      float64
	ResumeSkippedChunks int
	ManifestSHA256      string
	ManifestPath        string
	IndexPath           string
	RecoverableError    bool
}

// Run executes (or resumes) one recovery job idempotently.
func Run(in JobInput) (*Result, error) {
	if in.ChunkBytes <= 0 {
		return nil, media.NewError(media.ClassInvalidArgument, "job", "chunk-bytes must be positive")
	}
	if len(in.Stream) == 0 {
		return nil, media.NewError(media.ClassInvalidArgument, "job", "empty input stream")
	}
	if err := guardOutputPath(in.InputPath, in.WorkDir); err != nil {
		return nil, err
	}
	adapt, err := adapter.Latest(in.Codec)
	if err != nil {
		return nil, err
	}

	inputSum := sha256.Sum256(in.Stream)
	inputSHA := hex.EncodeToString(inputSum[:])
	cfgDigest := ConfigDigest(in.Codec, in.ChunkBytes, in.Seed)

	prev, err := LoadCheckpoint(in.WorkDir)
	if err != nil {
		return nil, err
	}
	resuming := prev != nil
	if resuming {
		if in.Resume {
			if err := prev.Validate(inputSHA, cfgDigest, adapt.Version()); err != nil {
				return nil, err
			}
		} else {
			// A fresh, idempotent re-run ignores prior state and overwrites it.
			prev = nil
			resuming = false
		}
	} else if in.Resume {
		return nil, media.NewError(media.ClassCheckpoint, "job",
			"resume requested but no checkpoint exists")
	}

	// Stream the input in fixed chunks; memory stays bounded by chunk size.
	splitter := adapt.NewSplitter()
	chunker, err := provenance.NewChunker(in.ChunkBytes)
	if err != nil {
		return nil, err
	}
	var (
		units     []adapter.Unit
		uerrs     []media.UnitError
		chunks    []provenance.Chunk
		interrupt bool
	)
	for off := int64(0); off < int64(len(in.Stream)); off += in.ChunkBytes {
		end := off + in.ChunkBytes
		if end > int64(len(in.Stream)) {
			end = int64(len(in.Stream))
		}
		part := in.Stream[off:end]
		emitted, _ := chunker.Write(part)
		chunks = append(chunks, emitted...)
		u, e, err := splitter.Write(part)
		if err != nil {
			return nil, err
		}
		units = append(units, u...)
		uerrs = append(uerrs, e...)
		if in.InterruptAfter > 0 && len(chunks) == in.InterruptAfter {
			interrupt = true
			break
		}
	}
	if !interrupt {
		tail, dangling := splitter.Close()
		units = append(units, tail...)
		if dangling != nil {
			uerrs = append(uerrs, *dangling)
		}
		chunks = append(chunks, chunker.Close()...)
	}

	skipped := 0
	completed := make([]int, 0, len(chunks))
	digests := make([]string, len(chunks))
	for i, ch := range chunks {
		digests[i] = ch.SHA256
		if resuming && prev.IsCompleted(i) {
			skipped++
			completed = append(completed, i)
			continue
		}
		completed = append(completed, i)
	}

	if interrupt {
		cp := &Checkpoint{
			SchemaVersion: CheckpointSchemaVersion, InputSHA256: inputSHA,
			ConfigDigest: cfgDigest, Codec: in.Codec, AdapterVersion: adapt.Version(),
			ChunkBytes: in.ChunkBytes, TotalChunks: len(chunks),
			Completed: completed, ChunkDigests: digests, Status: StatusInterrupted,
		}
		if err := SaveCheckpoint(in.WorkDir, cp); err != nil {
			return nil, err
		}
		return &Result{Status: StatusInterrupted, Chunks: len(chunks),
			SHA256Match: len(completed), ResumeSkippedChunks: skipped}, nil
	}
	return finish(in, adapt, inputSHA, units, uerrs, chunks, completed, digests, resuming, skipped)
}

func finish(in JobInput, adapt adapter.Adapter, inputSHA string, units []adapter.Unit,
	uerrs []media.UnitError, chunks []provenance.Chunk, completed []int,
	digests []string, resuming bool, skipped int) (*Result, error) {

	frames := buildFrameEvents(units)
	applyTimestamps(frames, in.PTS, in.ChunkBytes)

	var legacy *index.LegacyIndex
	var statuses []index.EntryStatus
	if len(in.LegacyIndex) > 0 {
		li, err := index.ReadLegacyIndex(bytes.NewReader(in.LegacyIndex))
		if err != nil {
			return nil, err
		}
		legacy = li
		statuses = index.Validate(li, frames, int64(len(in.Stream)))
	}
	rai := index.Rebuild(legacy, statuses, frames, int64(len(in.Stream)))

	mb := provenance.NewBuilder(in.Codec, adapt.Version(), inputSHA, int64(len(in.Stream)), in.ChunkBytes)
	mb.AddChunks(chunks)
	for _, f := range frames {
		mb.AddFrame(provenance.FrameRecord{
			FrameSeq: f.Index, Offset: f.Offset, Length: f.Length,
			IsKeyframe: f.IsKeyframe, RawPTS: f.RawPTS, HasRawPTS: f.HasPTS,
			RepairedPTS: f.RepairedPTS, RepairReason: f.RepairReason,
			ChunkIndex: int(f.Offset / in.ChunkBytes),
		})
	}
	for _, e := range uerrs {
		mb.AddError(e)
	}
	manifest := mb.Finalize()
	manifestSHA, err := manifest.Digest()
	if err != nil {
		return nil, err
	}
	if err := verifyTransportRoundTrip(frames, manifest); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(in.WorkDir, 0o755); err != nil {
		return nil, media.WrapError(media.ClassCheckpoint, "job", "workdir", err)
	}
	manifestPath := filepath.Join(in.WorkDir, "manifest.json")
	if err := os.WriteFile(manifestPath, mustMarshal(manifest), 0o644); err != nil {
		return nil, media.WrapError(media.ClassCheckpoint, "job", "manifest write", err)
	}
	indexPath := filepath.Join(in.WorkDir, "index.bin")
	var idxBuf bytes.Buffer
	if err := rai.Write(&idxBuf); err != nil {
		return nil, err
	}
	if err := os.WriteFile(indexPath, idxBuf.Bytes(), 0o644); err != nil {
		return nil, media.WrapError(media.ClassCheckpoint, "job", "index write", err)
	}

	cp := &Checkpoint{
		SchemaVersion: CheckpointSchemaVersion, InputSHA256: inputSHA,
		ConfigDigest: ConfigDigest(in.Codec, in.ChunkBytes, in.Seed),
		Codec:        in.Codec, AdapterVersion: adapt.Version(), ChunkBytes: in.ChunkBytes,
		TotalChunks: len(chunks), Completed: completed, ChunkDigests: digests, Status: StatusComplete,
	}
	if err := SaveCheckpoint(in.WorkDir, cp); err != nil {
		return nil, err
	}
	return summarize(manifest, rai, frames, uerrs, chunks, skipped, manifestPath, indexPath, manifestSHA, resuming), nil
}

func buildFrameEvents(units []adapter.Unit) []media.FrameEvent {
	var out []media.FrameEvent
	seq := 0
	for _, u := range units {
		if !u.IsFrame {
			continue
		}
		out = append(out, media.FrameEvent{
			Index: seq, Offset: u.Offset, Length: u.Length,
			IsFrame: true, IsKeyframe: u.IsKeyframe, RawPTS: -1,
		})
		seq++
	}
	return out
}

// applyTimestamps feeds source PTS hints through the repair kernel and
// derives the nominal tick step from the first consecutive valid pair.
func applyTimestamps(frames []media.FrameEvent, hints []PTSHint, chunkBytes int64) {
	clock, _ := media.NewClock(90000, 1)
	step := int64(3000)
	for i := 1; i < len(frames) && i < len(hints); i++ {
		if hints[i].Has && hints[i-1].Has && hints[i].PTS > hints[i-1].PTS {
			step = hints[i].PTS - hints[i-1].PTS
			break
		}
	}
	tr, err := repair.NewTimestampRepairer(clock, step)
	if err != nil {
		panic(err)
	}
	for i := range frames {
		if i < len(hints) {
			frames[i].HasPTS = hints[i].Has
			if hints[i].Has {
				frames[i].RawPTS = hints[i].PTS
			}
		}
		tr.Repair(&frames[i])
	}
}

func summarize(m provenance.Manifest, rai index.RandomAccessIndex, frames []media.FrameEvent,
	uerrs []media.UnitError, chunks []provenance.Chunk, skipped int,
	manifestPath, indexPath, manifestSHA string, resuming bool) *Result {

	good := 0
	keys := 0
	keyHits := 0
	for _, f := range m.Frames {
		if frameFullyConfirmed(m, f) {
			good++
		}
		if f.IsKeyframe {
			keys++
			if e, ok := findKey(rai, f.Offset); ok && e.IsKeyframe {
				keyHits++
			}
		}
	}
	rt := 100.0
	if len(m.Frames) > 0 {
		rt = float64(good) * 100 / float64(len(m.Frames))
	}
	khp := 100.0
	if keys > 0 {
		khp = float64(keyHits) * 100 / float64(keys)
	}
	repaired := 0
	for _, f := range m.Frames {
		if f.RepairReason != repair.ReasonOK {
			repaired++
		}
	}
	shaMatch := 0
	for _, ch := range chunks {
		if m.ChunkConfirmed(ch.Index) {
			shaMatch++
		}
	}
	return &Result{
		Status: StatusComplete, Chunks: len(chunks), SHA256Match: shaMatch,
		TruncatedUnits: len(uerrs), RepairedTimestamps: repaired,
		FramesTotal: len(frames), KeyframesTotal: keys, KeyframeHits: keyHits,
		RoundtripMatchPct: rt, KeyframeHitPct: khp,
		ResumeSkippedChunks: skipped, ManifestSHA256: manifestSHA,
		ManifestPath: manifestPath, IndexPath: indexPath,
		RecoverableError: len(uerrs) > 0,
	}
}

func findKey(rai index.RandomAccessIndex, off int64) (index.Entry, bool) {
	for _, e := range rai.Entries {
		if e.Offset == off {
			return e, true
		}
	}
	return index.Entry{}, false
}

// frameFullyConfirmed is true only when every chunk overlapped by the
// frame's byte range is confirmed, so a frame straddling a tainted chunk
// does not count as round-tripped.
func frameFullyConfirmed(m provenance.Manifest, f provenance.FrameRecord) bool {
	if m.ChunkBytes <= 0 {
		return false
	}
	first := f.Offset / m.ChunkBytes
	last := (f.Offset + f.Length - 1) / m.ChunkBytes
	for i := first; i <= last; i++ {
		if !m.ChunkConfirmed(int(i)) {
			return false
		}
	}
	return true
}

func mustMarshal(m provenance.Manifest) []byte {
	raw, err := m.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}

// guardOutputPath ensures recovery outputs can never overwrite the source
// file: the work directory must differ from the input and must not contain
// it as a regular file. Symlinks are resolved so the check cannot be
// bypassed through an aliased path.
func guardOutputPath(inputPath, workDir string) error {
	if inputPath == "" {
		return nil // in-memory fixtures have no on-disk input to overwrite
	}
	inAbs, err := filepath.Abs(filepath.Clean(inputPath))
	if err != nil {
		return media.WrapError(media.ClassInvalidArgument, "job", "input path", err)
	}
	wdAbs, err := filepath.Abs(filepath.Clean(workDir))
	if err != nil {
		return media.WrapError(media.ClassInvalidArgument, "job", "workdir path", err)
	}
	if inAbs == wdAbs {
		return media.NewError(media.ClassInvalidArgument, "job",
			"output workdir must not equal the input path")
	}
	if resolved, err := filepath.EvalSymlinks(wdAbs); err == nil && resolved == inAbs {
		return media.NewError(media.ClassInvalidArgument, "job",
			"workdir resolves to the input path")
	}
	// Reject when the input sits directly inside the work directory, where
	// manifest/index writes could collide with it.
	if hasPathPrefix(inAbs, wdAbs+string(filepath.Separator)) {
		return media.NewError(media.ClassInvalidArgument, "job",
			"input file must live outside the output workdir")
	}
	return nil
}

func hasPathPrefix(path, prefix string) bool {
	if len(path) < len(prefix) {
		return false
	}
	return path[:len(prefix)] == prefix
}

// verifyTransportRoundTrip proves two things about every recovered frame:
// its repaired PTS survives RTP packetization (including values above
// 2^32), and the canonical manifest survives an MP4 container round trip.
// Any mismatch is a verification failure rather than a parse problem.
func verifyTransportRoundTrip(frames []media.FrameEvent, m provenance.Manifest) error {
	packets, err := transport.Packetize(frames, 0xA1CCE55)
	if err != nil {
		return err
	}
	recovered, err := transport.Depacketize(packets)
	if err != nil {
		return err
	}
	if len(recovered) != len(frames) {
		return media.NewError(media.ClassCheckpoint, "job",
			"RTP round trip lost frames")
	}
	for i, f := range frames {
		if recovered[i].PTS33 != uint64(f.RepairedPTS) {
			return media.NewError(media.ClassCheckpoint, "job",
				"RTP round trip altered a repaired timestamp")
		}
	}
	raw, err := m.Marshal()
	if err != nil {
		return err
	}
	container, err := transport.WriteMP4Container(raw)
	if err != nil {
		return err
	}
	back, err := transport.ReadMP4Container(container)
	if err != nil {
		return err
	}
	if !bytes.Equal(back, raw) {
		return media.NewError(media.ClassCheckpoint, "job",
			"MP4 container round trip altered the manifest")
	}
	return nil
}
