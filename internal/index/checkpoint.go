// Package index persists the processing manifest in fixed-size, checksummed
// blocks that can be resumed after a crash.
//
// The on-disk format is intentionally boring: a header with the format version
// and input digest, then blocks of [magic, length, seq, payload checksum,
// payload]. Every block is fsynced and the file is replaced atomically at
// commit points, so a crash can only ever leave a stale tail — never a torn
// block in the middle.
package index

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"

	"avbatch/internal/mediaerr"
)

// FormatVersion marks the block layout. Readers refuse anything else.
const FormatVersion uint32 = 1

const (
	fileMagic  = "AVBI"
	blockMagic = 0x4156424B         // "AVBK"
	headerSize = 4 + 4 + 32 + 8 + 4 // magic, version, input digest, block size, reserved
	// Block prefix: magic(4) length(4) seq(4) crc32(4) payload sha256(32).
	blockPrefix = 4 + 4 + 4 + 4 + 32
	shaSize     = 32
)

// Entry is one manifest row. Field order and JSON tags are part of the format:
// deterministic output depends on them.
type Entry struct {
	Index        int     `json:"index"`
	Kind         string  `json:"kind"`
	StartNanos   int64   `json:"start_ns"`
	EndNanos     int64   `json:"end_ns"`
	SampleCount  int     `json:"samples"`
	Bytes        int64   `json:"bytes"`
	Source       []int   `json:"source,omitempty"`
	SilenceNanos int64   `json:"silence_ns,omitempty"`
	Fingerprint  string  `json:"fingerprint,omitempty"`
	DuplicateOf  int     `json:"duplicate_of,omitempty"`
	Reason       string  `json:"reason,omitempty"`
	TargetKbps   float64 `json:"target_kbps,omitempty"`
	ActualKbps   float64 `json:"actual_kbps,omitempty"`
}

// Block is one committed chunk of entries.
type Block struct {
	Seq        uint64  `json:"seq"`
	FirstEntry int     `json:"first_entry"`
	Entries    []Entry `json:"entries"`
}

// Header describes the whole run and is what resume validation compares.
type Header struct {
	Version     uint32
	InputDigest [32]byte
	BlockSize   int
	Blocks      uint64
	// EntryCount is the total number of entries committed.
	EntryCount int
	// OffsetBytes is the byte offset of the first uncommitted entry
	// (i.e. the end of the last complete block).
	OffsetBytes int64
}

// DigestString renders the input digest for reports.
func (h Header) DigestString() string {
	return fmt.Sprintf("%x", h.InputDigest[:])
}

// Config controls block sizing and the on-disk path.
type Config struct {
	// BlockSamples is the media sample budget of one block. Entries are never
	// split across blocks, so a block may slightly exceed the budget when the
	// last entry is large.
	BlockSamples int
	// Path is the index file. Empty disables persistence (memory only).
	Path string
	// InputDigest identifies the source group; resume refuses a mismatch.
	InputDigest [32]byte
	// MaxBlockSamples bounds BlockSamples against hostile configs.
	MaxBlockSamples int
}

// DefaultConfig returns a config with a 128-sample block budget and a 1e6 cap.
func DefaultConfig() Config {
	return Config{BlockSamples: 128, MaxBlockSamples: 1 << 20}
}

func (c Config) validated() (Config, error) {
	if c.BlockSamples <= 0 {
		return c, errors.New("index: BlockSamples must be positive")
	}
	if c.MaxBlockSamples <= 0 {
		c.MaxBlockSamples = 1 << 20
	}
	if c.BlockSamples > c.MaxBlockSamples {
		return c, fmt.Errorf("index: BlockSamples %d exceeds cap %d", c.BlockSamples, c.MaxBlockSamples)
	}
	return c, nil
}

// Writer commits blocks.
type Writer struct {
	cfg   Config
	file  *os.File
	buf   bytes.Buffer
	hdr   Header
	block Block
	// pendingSamples is the sample budget consumed by the current block.
	pendingSamples int
	closed         bool
}

// NewWriter creates (or truncates) the index and writes the header durably.
func NewWriter(cfg Config) (*Writer, error) {
	cfg, err := cfg.validated()
	if err != nil {
		return nil, err
	}
	w := &Writer{
		cfg: cfg,
		hdr: Header{Version: FormatVersion, InputDigest: cfg.InputDigest, BlockSize: cfg.BlockSamples},
	}
	if cfg.Path == "" {
		return w, nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(cfg.Path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	w.file = f
	if err := w.writeHeader(); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

func (w *Writer) writeHeader() error {
	var h [headerSize]byte
	copy(h[0:], fileMagic)
	binary.BigEndian.PutUint32(h[4:], FormatVersion)
	copy(h[8:40], w.cfg.InputDigest[:])
	binary.BigEndian.PutUint64(h[40:48], uint64(w.cfg.BlockSamples))
	if _, err := w.file.WriteAt(h[:], 0); err != nil {
		return err
	}
	return nil
}

// Add appends one entry, flushing when the block's sample budget is reached.
// It returns the seq of the block that was just committed, or zero when
// nothing was flushed.
func (w *Writer) Add(e Entry) (uint64, error) {
	if w.closed {
		return 0, errors.New("index: writer is closed")
	}
	if len(w.block.Entries) == 0 {
		w.block.FirstEntry = w.hdr.EntryCount
	}
	e.Index = w.hdr.EntryCount
	w.block.Entries = append(w.block.Entries, e)
	w.hdr.EntryCount++
	w.pendingSamples += e.SampleCount
	if w.pendingSamples >= w.cfg.BlockSamples {
		return w.Flush()
	}
	return 0, nil
}

// Flush commits the pending block. An empty block is a no-op.
func (w *Writer) Flush() (uint64, error) {
	if len(w.block.Entries) == 0 {
		return 0, nil
	}
	w.pendingSamples = 0
	if w.file == nil {
		w.hdr.Blocks++
		w.block = Block{}
		return w.hdr.Blocks, nil
	}
	seq := w.hdr.Blocks
	payload, err := json.Marshal(struct {
		Seq        uint64  `json:"seq"`
		FirstEntry int     `json:"first_entry"`
		Entries    []Entry `json:"entries"`
	}{seq, w.block.FirstEntry, w.block.Entries})
	if err != nil {
		return 0, err
	}
	rec := encodeBlock(seq, payload)
	if _, err := w.file.WriteAt(rec, w.hdr.OffsetBytes+headerSize); err != nil {
		return 0, err
	}
	w.hdr.OffsetBytes += int64(len(rec))
	w.hdr.Blocks++
	w.block = Block{}
	if err := w.file.Sync(); err != nil {
		return 0, err
	}
	return seq, nil
}

func encodeBlock(seq uint64, payload []byte) []byte {
	rec := make([]byte, blockPrefix+len(payload))
	binary.BigEndian.PutUint32(rec[0:], blockMagic)
	binary.BigEndian.PutUint32(rec[4:], uint32(len(payload)))
	binary.BigEndian.PutUint32(rec[8:], uint32(seq))
	sum := sha256.Sum256(payload)
	copy(rec[16:16+shaSize], sum[:])
	binary.BigEndian.PutUint32(rec[12:], prefixChecksum(rec))
	copy(rec[blockPrefix:], payload)
	return rec
}

// prefixChecksum covers the sequence number and payload digest, but not the
// checksum field itself.
func prefixChecksum(rec []byte) uint32 {
	var buf [4 + shaSize]byte
	copy(buf[0:], rec[8:12])
	copy(buf[4:], rec[16:16+shaSize])
	return crc32.ChecksumIEEE(buf[:])
}

// Commit flushes the tail block and atomically replaces the index file.
func (w *Writer) Commit() (Header, error) {
	if w.closed {
		return w.hdr, errors.New("index: writer is closed")
	}
	if _, err := w.Flush(); err != nil {
		return w.hdr, err
	}
	w.closed = true
	if w.file == nil {
		return w.hdr, nil
	}
	// Rewrite the header with the final counts, then fsync and rename.
	var h [headerSize]byte
	copy(h[0:], fileMagic)
	binary.BigEndian.PutUint32(h[4:], FormatVersion)
	copy(h[8:40], w.cfg.InputDigest[:])
	binary.BigEndian.PutUint64(h[40:48], uint64(w.cfg.BlockSamples))
	if _, err := w.file.WriteAt(h[:], 0); err != nil {
		return w.hdr, err
	}
	if err := w.file.Sync(); err != nil {
		return w.hdr, err
	}
	if err := w.file.Close(); err != nil {
		return w.hdr, err
	}
	if err := os.Rename(w.cfg.Path+".tmp", w.cfg.Path); err != nil {
		return w.hdr, err
	}
	return w.hdr, nil
}

// Close abandons uncommitted work but keeps already-committed blocks. The
// temporary file is left in place so a later resume can salvage it.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

// Reader reads and validates a checkpoint.
type Reader struct {
	hdr    Header
	blocks []Block
	// Entries is the recovered entry list, in order.
	Entries []Entry
	// Recovery describes what was found and what was salvageable.
	Recovery Recovery
}

// Recovery reports resume state for the CLI.
type Recovery struct {
	BlocksFound int
	// BlocksValid counts blocks that passed their checksums and were reused
	// or retained. TotalBlocks is how many blocks the index holds afterwards.
	BlocksValid    int
	TotalBlocks    int
	EntriesFound   int
	ResumeOffset   int64
	Corrupt        bool
	Truncated      bool
	DigestMismatch bool
	// Kinds lists detected problems for reporting.
	Kinds []string
	// Err is the joined error; valid blocks remain readable.
	Err error
}

// Open validates path against wantDigest. It returns a reader even when the
// tail is damaged, so callers can keep the verified prefix.
func Open(path string, wantDigest [32]byte) (*Reader, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r := &Reader{}
	return r, r.parse(data, wantDigest)
}

func (r *Reader) parse(data []byte, wantDigest [32]byte) error {
	var errs []error
	if len(data) < headerSize {
		r.Recovery.Corrupt = true
		errs = append(errs, mediaerr.New(mediaerr.KindCorruptCheckpoint, -1, "index", 0,
			fmt.Sprintf("file is %d bytes, shorter than the %d-byte header", len(data), headerSize)))
		r.Recovery.Kinds = append(r.Recovery.Kinds, string(mediaerr.KindCorruptCheckpoint))
		r.Recovery.Err = errors.Join(errs...)
		return r.Recovery.Err
	}
	if string(data[0:4]) != fileMagic {
		r.Recovery.Corrupt = true
		errs = append(errs, mediaerr.New(mediaerr.KindCorruptCheckpoint, -1, "index", 0, "bad file magic"))
	} else {
		r.hdr.Version = binary.BigEndian.Uint32(data[4:8])
		copy(r.hdr.InputDigest[:], data[8:40])
		r.hdr.BlockSize = int(binary.BigEndian.Uint64(data[40:48]))
		if r.hdr.Version != FormatVersion {
			errs = append(errs, mediaerr.New(mediaerr.KindCorruptCheckpoint, -1, "index", 4,
				fmt.Sprintf("format version %d, want %d", r.hdr.Version, FormatVersion)))
		}
	}
	if r.hdr.InputDigest != wantDigest {
		r.Recovery.DigestMismatch = true
		errs = append(errs, mediaerr.New(mediaerr.KindResumeMismatch, -1, "index", 8,
			fmt.Sprintf("checkpoint input digest %x does not match input %x",
				r.hdr.InputDigest[:8], wantDigest[:8])))
	}
	r.hdr.OffsetBytes = 0

	off := int64(headerSize)
	for {
		if off == int64(len(data)) {
			break
		}
		if int64(len(data))-off < blockPrefix {
			r.Recovery.Truncated = true
			errs = append(errs, mediaerr.New(mediaerr.KindTruncatedIndex, -1, "block", off,
				fmt.Sprintf("%d trailing bytes cannot hold a block prefix", int64(len(data))-off)))
			break
		}
		rec := data[off:]
		if binary.BigEndian.Uint32(rec[0:]) != blockMagic {
			r.Recovery.Corrupt = true
			errs = append(errs, mediaerr.New(mediaerr.KindCorruptCheckpoint, -1, "block", off, "bad block magic"))
			break
		}
		length := int64(binary.BigEndian.Uint32(rec[4:]))
		seq := binary.BigEndian.Uint32(rec[8:])
		if length <= 0 || length > int64(len(data))-off-blockPrefix {
			r.Recovery.Truncated = true
			errs = append(errs, mediaerr.New(mediaerr.KindTruncatedIndex, -1, "block", off,
				fmt.Sprintf("block %d declares %d payload bytes, %d remain", seq, length,
					int64(len(data))-off-blockPrefix)))
			break
		}
		crc := binary.BigEndian.Uint32(rec[12:])
		if prefixChecksum(rec) != crc {
			r.Recovery.Corrupt = true
			errs = append(errs, mediaerr.New(mediaerr.KindCorruptCheckpoint, -1, "block", off,
				fmt.Sprintf("block %d prefix checksum mismatch", seq)))
			break
		}
		payload := rec[blockPrefix : blockPrefix+length]
		sum := sha256.Sum256(payload)
		if !bytes.Equal(sum[:], rec[16:16+shaSize]) {
			r.Recovery.Corrupt = true
			errs = append(errs, mediaerr.New(mediaerr.KindChecksumMismatch, -1, "block", off,
				fmt.Sprintf("block %d payload checksum mismatch", seq)))
			break
		}
		var b Block
		if err := json.Unmarshal(payload, &b); err != nil {
			r.Recovery.Corrupt = true
			errs = append(errs, mediaerr.Wrap(mediaerr.KindCorruptCheckpoint, -1, "block", off,
				fmt.Sprintf("block %d payload is not valid JSON", seq), err))
			break
		}
		if b.Seq != uint64(seq) || uint64(r.hdr.Blocks) != b.Seq {
			r.Recovery.Corrupt = true
			errs = append(errs, mediaerr.New(mediaerr.KindManifestOrder, -1, "block", off,
				fmt.Sprintf("block sequence %d is not the expected %d", b.Seq, r.hdr.Blocks)))
			break
		}
		if b.FirstEntry != len(r.Entries) {
			r.Recovery.Corrupt = true
			errs = append(errs, mediaerr.New(mediaerr.KindManifestOrder, -1, "block", off,
				fmt.Sprintf("block %d starts at entry %d, expected %d", b.Seq, b.FirstEntry, len(r.Entries))))
			break
		}
		r.blocks = append(r.blocks, b)
		r.Entries = append(r.Entries, b.Entries...)
		r.hdr.Blocks++
		r.hdr.EntryCount = len(r.Entries)
		off += blockPrefix + length
		r.hdr.OffsetBytes = off - headerSize
		r.Recovery.BlocksValid++
	}
	r.Recovery.BlocksFound = len(r.blocks)
	r.Recovery.TotalBlocks = len(r.blocks)
	r.Recovery.EntriesFound = len(r.Entries)
	r.Recovery.ResumeOffset = r.hdr.OffsetBytes
	if len(errs) > 0 {
		for _, kind := range mediaerr.Kinds(errors.Join(errs...)) {
			r.Recovery.Kinds = append(r.Recovery.Kinds, string(kind))
		}
		r.Recovery.Err = errors.Join(errs...)
	}
	return r.Recovery.Err
}

// Header returns the parsed header.
func (r *Reader) Header() Header { return r.hdr }

// ResumeFrom returns the number of entries that can be skipped when
// continuing. Only a clean, digest-matching checkpoint is resumable.
func (r *Reader) ResumeFrom(wantDigest [32]byte) (int, error) {
	if r.Recovery.DigestMismatch {
		return 0, mediaerr.New(mediaerr.KindResumeMismatch, -1, "index", 0,
			"input digest changed; refusing to append to an index built from other media")
	}
	if r.Recovery.Corrupt || r.Recovery.Truncated {
		return 0, mediaerr.New(mediaerr.KindTruncatedIndex, -1, "index", r.Recovery.ResumeOffset,
			fmt.Sprintf("checkpoint is incomplete at offset %d; only verified blocks are reusable",
				r.Recovery.ResumeOffset))
	}
	return len(r.Entries), nil
}

// ManifestDigest hashes the ordered entry list. Identical inputs must produce
// an identical digest; it is the repeat-run acceptance check.
func ManifestDigest(entries []Entry) ([32]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(entries); err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(buf.Bytes()), nil
}
