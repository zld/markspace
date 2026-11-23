// Package remux rewrites parsed CMAF fragments into per-slot output
// streams and a manifest without ever decoding or re-encoding media.
// Compressed sample payloads are spooled to disk as they arrive so that
// memory stays bounded regardless of the total input volume; finalization
// reads one fragment at a time in the corrected, deterministic order.
package remux

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediatool/internal/mp4"
	"mediatool/internal/timeline"
)

// SafePath joins root with name and rejects any name that is absolute,
// uses volume/UNC forms, or escapes root via "..". The returned path is
// always contained within root.
func SafePath(root, name string) (string, error) {
	if filepath.IsAbs(name) || strings.ContainsRune(name, 0) {
		return "", timeline.NewError(timeline.ClassUnsafePath,
			fmt.Sprintf("absolute or NUL-containing path rejected: %q", name))
	}
	// Reject any parent-directory segment before cleaning, because
	// filepath.Clean would otherwise silently collapse it.
	for _, seg := range strings.FieldsFunc(filepath.ToSlash(name), func(r rune) bool {
		return r == '/'
	}) {
		if seg == ".." {
			return "", timeline.NewError(timeline.ClassUnsafePath,
				fmt.Sprintf("parent-directory segment rejected in %q", name))
		}
	}
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", timeline.NewError(timeline.ClassUnsafePath, err.Error())
	}
	joined := filepath.Join(cleanRoot, filepath.FromSlash(filepath.Clean("/"+name)))
	rel, err := filepath.Rel(cleanRoot, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", timeline.NewError(timeline.ClassUnsafePath,
			fmt.Sprintf("path %q escapes output root", name))
	}
	return joined, nil
}

// SpoolMeta is the bounded, payload-free metadata kept per fragment.
type SpoolMeta struct {
	Slot            string
	OrderKey        int // deterministic ordering (recomputed InputOrder)
	InputOrder      int // original arrival position
	Seq             int // original mfhd seq, -1 if missing
	TrackID         uint32
	CorrectedDTS    uint64
	BaseMediaDecode uint64
	Timescale       uint32
	SampleDurations []uint32
	SampleSizes     []uint32
	Repeated        bool
	Late            bool
	// PayloadStart/PayloadEnd bound this slot's bytes inside the spooled
	// mdat payload. When equal to the full spool they are zero/len.
	PayloadStart int
	PayloadEnd   int
	PayloadHash  [32]byte
	SpoolFile    string // payload spool path, relative to root
}

// Writer accumulates fragments for one output root.
type Writer struct {
	root     string
	spoolDir string
	metas    []SpoolMeta
}

// NewWriter prepares root and its spool directory.
func NewWriter(root string) (*Writer, error) {
	safe, err := SafePath(root, ".")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(safe, 0o755); err != nil {
		return nil, fmt.Errorf("create output root: %w", err)
	}
	spool, err := SafePath(safe, "spool")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(spool, 0o755); err != nil {
		return nil, err
	}
	return &Writer{root: safe, spoolDir: spool}, nil
}

// Root returns the validated absolute output root.
func (w *Writer) Root() string { return w.root }

// Spool stores one fragment's compressed payload on disk and records its
// metadata. The payload bytes are streamed to a file, never accumulated,
// so concurrent in-memory state is O(number of metadata records), not
// O(total media bytes).
func (w *Writer) Spool(meta SpoolMeta, payload io.Reader, payloadLen int) error {
	name := fmt.Sprintf("%s-%08d.payload", sanitize(meta.Slot), meta.OrderKey)
	rel := filepath.Join("spool", name)
	full, err := SafePath(w.root, rel)
	if err != nil {
		return err
	}
	f, err := os.Create(full)
	if err != nil {
		return fmt.Errorf("create spool file: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(payload, int64(payloadLen)))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("spool payload: %w", err)
	}
	if int(n) != payloadLen {
		return timeline.ErrorAt(timeline.ClassBoxTruncated, -1,
			fmt.Sprintf("spool got %d of %d payload bytes", n, payloadLen))
	}
	var hash [32]byte
	copy(hash[:], h.Sum(nil))
	if meta.PayloadHash != [32]byte{} && meta.PayloadHash != hash {
		return timeline.NewError(timeline.ClassOverflow, "spooled payload hash mismatch")
	}
	meta.PayloadHash = hash
	if meta.PayloadEnd == 0 && meta.PayloadStart == 0 {
		meta.PayloadEnd = payloadLen
	}
	if meta.PayloadEnd > payloadLen || meta.PayloadStart > meta.PayloadEnd {
		return timeline.NewError(timeline.ClassOverflow, "spool payload slice out of range")
	}
	meta.SpoolFile = rel
	w.metas = append(w.metas, meta)
	return nil
}

// FinalizePlan groups spool metadata by slot in deterministic order.
type FinalizePlan struct {
	Slot  string
	Track mp4.TrackInfo
	Metas []SpoolMeta
}

// Finalize rewrites one init segment and one numbered chunk stream per
// slot, then emits the manifest. Fragment sequence numbers are made
// contiguous and payloads are copied byte-identical from the spool.
func (w *Writer) Finalize(plans []FinalizePlan) (*Manifest, error) {
	manifest := NewManifest()
	for _, plan := range plans {
		if err := w.finalizeSlot(plan, manifest); err != nil {
			return nil, err
		}
	}
	manifest.sortSlots()
	if err := w.writeManifest(manifest); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(w.spoolDir); err != nil {
		return nil, fmt.Errorf("remove spool: %w", err)
	}
	manifest.SummaryHash = manifest.computeSummaryHash()
	// Re-write once now that the summary hash is known.
	if err := w.writeManifest(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func (w *Writer) finalizeSlot(plan FinalizePlan, manifest *Manifest) error {
	slotDir, err := SafePath(w.root, sanitize(plan.Slot))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(slotDir, 0o755); err != nil {
		return err
	}
	// Init segment: deterministic synthesis from track metadata.
	initPath, err := SafePath(slotDir, "init.mp4")
	if err != nil {
		return err
	}
	if err := os.WriteFile(initPath, mp4.BuildInit([]mp4.TrackInfo{plan.Track}), 0o644); err != nil {
		return err
	}

	metas := append([]SpoolMeta(nil), plan.Metas...)
	sort.SliceStable(metas, func(i, j int) bool {
		if metas[i].CorrectedDTS != metas[j].CorrectedDTS {
			return metas[i].CorrectedDTS < metas[j].CorrectedDTS
		}
		return metas[i].OrderKey < metas[j].OrderKey
	})

	slotEntry := SlotManifest{
		Slot:      plan.Slot,
		TrackID:   plan.Track.TrackID,
		MediaType: plan.Track.MediaType,
		CodecKey:  plan.Track.CodecKey,
		Timescale: plan.Track.Timescale,
		InitFile:  filepath.Join(sanitize(plan.Slot), "init.mp4"),
	}
	for outSeq, meta := range metas {
		fullPayload, perr := w.readSpool(meta.SpoolFile)
		if perr != nil {
			return perr
		}
		payload := fullPayload
		hashToRecord := meta.PayloadHash
		if meta.PayloadStart != 0 || meta.PayloadEnd != len(fullPayload) {
			payload = fullPayload[meta.PayloadStart:meta.PayloadEnd]
			hashToRecord = sha256.Sum256(payload)
		}
		spec := mp4.FragmentSpec{
			Seq:             outSeq, // contiguous, deterministic renumbering
			TrackID:         plan.Track.TrackID,
			BaseMediaDecode: meta.CorrectedDTS,
			SampleDurations: meta.SampleDurations,
			SampleSizes:     meta.SampleSizes,
			SamplePayload:   payload,
		}
		chunk := mp4.BuildFragment(spec)
		chunkName := fmt.Sprintf("chunk-%06d.m4s", outSeq)
		chunkPath, cerr := SafePath(slotDir, chunkName)
		if cerr != nil {
			return cerr
		}
		if err := os.WriteFile(chunkPath, chunk, 0o644); err != nil {
			return err
		}
		var totalDuration uint64
		for _, d := range meta.SampleDurations {
			totalDuration += uint64(d)
		}
		slotEntry.Fragments = append(slotEntry.Fragments, FragmentManifest{
			OutputSeq:      outSeq,
			OriginSeq:      meta.Seq,
			InputOrder:     meta.InputOrder,
			File:           filepath.Join(sanitize(plan.Slot), chunkName),
			CorrectedDTS:   meta.CorrectedDTS,
			Duration:       totalDuration,
			PayloadBytes:   len(payload),
			Timescale:      plan.Track.Timescale,
			SampleCount:    len(meta.SampleSizes),
			Bytes:          len(chunk),
			Repeated:       meta.Repeated,
			Late:           meta.Late,
			PayloadHashHex: hex.EncodeToString(hashToRecord[:]),
		})
	}
	manifest.Slots = append(manifest.Slots, slotEntry)
	return nil
}

func (w *Writer) readSpool(rel string) ([]byte, error) {
	full, err := SafePath(w.root, rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, fmt.Errorf("open spool: %w", err)
	}
	defer f.Close()
	return io.ReadAll(f)
}

func sanitize(name string) string {
	name = filepath.FromSlash(name)
	name = strings.ReplaceAll(name, "..", "_")
	name = strings.ReplaceAll(name, string(os.PathSeparator), "_")
	name = strings.TrimSpace(name)
	if name == "" {
		name = "slot"
	}
	return name
}

// Metas exposes the recorded metadata for tests and metrics.
func (w *Writer) Metas() []SpoolMeta {
	out := make([]SpoolMeta, len(w.metas))
	copy(out, w.metas)
	return out
}

// ErrNoFragments is returned when finalization has nothing to write.
var ErrNoFragments = errors.New("no fragments to finalize")
