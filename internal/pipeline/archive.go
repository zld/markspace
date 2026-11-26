package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/manifest"
	"example.com/mediavault/internal/model"
	"example.com/mediavault/internal/packageindex"
)

// maxArchiveChunks caps a single archive's block table. Beyond this the stco
// entry count stops being meaningful for our 1 MiB block policy anyway.
const maxArchiveChunks = 1 << 20

// blockSource streams stored block objects one at a time so that archiving
// never holds more than one block in memory.
type blockSource struct {
	store   objectOpener
	digests []string
	current io.ReadCloser
	index   int
}

type objectOpener interface {
	OpenObject(digest string) (*os.File, error)
}

func (b *blockSource) Read(p []byte) (int, error) {
	for {
		if b.current == nil {
			if b.index >= len(b.digests) {
				return 0, io.EOF
			}
			f, err := b.store.OpenObject(b.digests[b.index])
			if err != nil {
				return 0, err
			}
			b.current = f
			b.index++
		}
		n, err := b.current.Read(p)
		if n > 0 {
			return n, nil
		}
		if err == io.EOF {
			b.current.Close()
			b.current = nil
			continue
		}
		if err != nil {
			return 0, err
		}
	}
}

func (b *blockSource) Close() error {
	if b.current != nil {
		return b.current.Close()
	}
	return nil
}

// archive wraps the derived PCM blocks in a deterministic MP4, stores it by
// content, re-scans the stored wrapper and records a manifest entry. Packaging
// or source corruption is rejected before anything is committed.
func (r *Runner) archive(rep Report, refs []model.ChunkRef, extra *manifest.RTPInfo) Report {
	a := rep.Asset
	digests := make([]string, len(refs))
	offsets := make([]int64, len(refs))
	frames := make([]int64, len(refs))
	var total int64
	for i, c := range refs {
		digests[i] = c.Digest
		offsets[i] = total
		frames[i] = c.Frames
		total += c.Length
	}
	if len(refs) > maxArchiveChunks {
		return r.reject(rep, model.StageNormalized, fault.New(fault.CodeLimit, fault.StageNormalized, -1,
			"asset has %d blocks, above the %d the stco table supports", len(refs), maxArchiveChunks))
	}
	sampleRate := uint32(a.Track.SampleRate.Num / a.Track.SampleRate.Den)
	if sampleRate == 0 {
		sampleRate = 8000
	}
	spec := packageindex.ArchiveSpec{
		SampleRate:    sampleRate,
		Channels:      uint16(a.Track.Channels),
		BitsPerSample: uint16(a.Track.BitsPerSample),
		SampleCount:   uint32(a.Track.TotalFrames),
		Timescale:     sampleRate,
		Duration:      uint64(a.Track.TotalFrames),
		PayloadSize:   total,
		ChunkOffsets:  offsets,
		ChunkFrames:   frames,
	}
	if a.Transport == model.TransportRTP {
		spec.Handler = "meta"
	}
	tmp, err := os.CreateTemp(r.store.Root(), ".archive-*")
	if err != nil {
		return r.reject(rep, model.StageNormalized, fault.Wrap(fault.CodeIO, fault.StageNormalized, -1, err, "create archive scratch"))
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	h := sha256.New()
	sink := io.MultiWriter(tmp, h)
	src := &blockSource{store: r.store, digests: digests}
	_, werr := packageindex.WriteArchive(sink, spec, src)
	src.Close()
	if werr != nil {
		tmp.Close()
		return r.reject(rep, model.StageNormalized, werr)
	}
	if err := tmp.Close(); err != nil {
		return r.reject(rep, model.StageNormalized, fault.Wrap(fault.CodeIO, fault.StageNormalized, -1, err, "close archive scratch"))
	}
	digest := hex.EncodeToString(h.Sum(nil))
	st, statErr := os.Stat(tmpPath)
	if statErr != nil {
		return r.reject(rep, model.StageArchived, fault.Wrap(fault.CodeIO, fault.StageArchived, -1, statErr, "stat archive scratch"))
	}
	size := st.Size()
	rep.ArchiveDigest = digest
	if r.store.Exists(digest) {
		rep.Deduplicated = true
	}
	if err := r.putScratch(digest, tmpPath, size); err != nil {
		return r.reject(rep, model.StageArchived, err)
	}
	idx, err := packageindex.Scan(tmpPath)
	if err != nil {
		return r.reject(rep, model.StageArchived, err)
	}
	af, err := r.store.OpenObject(digest)
	if err != nil {
		return r.reject(rep, model.StageArchived, err)
	}
	err = packageindex.ValidateChunkOffsets(af, idx)
	af.Close()
	if err != nil {
		return r.reject(rep, model.StageArchived, err)
	}

	r.transition(&rep, a.Name, model.StageNormalized, model.StageArchived)
	a.Stage = model.StageArchived
	a.ArchiveDigest = digest
	rep.Asset = a

	entry := manifest.NewEntry(a, digest, size, idx.ContainerDigest)
	entry.RTP = extra
	if err := r.recordArchive(entry); err != nil {
		return r.reject(rep, model.StageArchived, err)
	}
	fmt.Fprintf(r.out, "archived %s key=%s archive=%s blocks=%d recomputed=%d reused=%d\n",
		a.Name, a.Key, digest[:12], len(refs), rep.Recomputed, rep.Reused)
	return r.appendReport(rep)
}

func (r *Runner) putScratch(digest, path string, size int64) error {
	if r.store.Exists(digest) {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fault.Wrap(fault.CodeIO, fault.StageArchived, -1, err, "open archive scratch")
	}
	defer f.Close()
	return r.store.PutReader(digest, f, size)
}
