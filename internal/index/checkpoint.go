package index

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zld/continuity/internal/clock"
)

// magic and version tag every committed chunk so a foreign or truncated file is
// rejected rather than silently resumed.
const (
	chunkMagic   = "CTIDX001"
	headerSize   = 8 + 32 + 8 + 32 // magic + fingerprint + seq + checksum
	tmpSuffix    = ".tmp"
	committedExt = ".chk"
)

// Fingerprint is a 32-byte digest of the input container. A changed input
// changes the fingerprint and therefore invalidates every checkpoint.
type Fingerprint [32]byte

func (f Fingerprint) String() string { return hex.EncodeToString(f[:]) }

// FingerprintFile hashes the named input to derive its fingerprint.
func FingerprintFile(path string) (Fingerprint, error) {
	f, err := os.Open(path)
	if err != nil {
		return Fingerprint{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return Fingerprint{}, err
	}
	var fp Fingerprint
	copy(fp[:], h.Sum(nil))
	return fp, nil
}

// Chunk is one serialized index block plus the metadata needed to validate it
// independently of the others.
type Chunk struct {
	Seq       uint64
	Fingerprint Fingerprint
	Entries   []Entry
}

// Store writes and validates chunk files inside a working directory.
type Store struct {
	dir        string
	fp         Fingerprint
	committed  map[uint64]bool
}

// NewStore prepares dir for chunk files.
func NewStore(dir string, fp Fingerprint) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir, fp: fp, committed: map[uint64]bool{}}, nil
}

// Fingerprint returns the input digest this store validates chunks against.
func (s *Store) Fingerprint() Fingerprint { return s.fp }

func chunkPath(dir string, seq uint64) string {
	return filepath.Join(dir, fmt.Sprintf("chunk-%06d%s", seq, committedExt))
}

// Write serializes a chunk to a temp file, fsyncs it, then atomically renames
// it into place. A crash at any point before the rename leaves no committed
// chunk, so an unfinished block is never mistaken for done work.
func (s *Store) Write(c Chunk) error {
	if c.Fingerprint != s.fp {
		return fmt.Errorf("%w: chunk %d fingerprint does not match store",
			clock.ErrResumeMismatch, c.Seq)
	}
	final := chunkPath(s.dir, c.Seq)
	tmp := final + tmpSuffix
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := encodeChunk(f, c); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return err
	}
	s.committed[c.Seq] = true
	return nil
}

// encodeChunk writes header then length-prefixed entries. The checksum is the
// SHA-256 of every payload byte that follows the header (the entry count plus
// all entry rows), so recovery can verify the on-disk bytes before decoding.
func encodeChunk(w io.Writer, c Chunk) error {
	var payload bytes.Buffer
	var lenb [4]byte
	binary.BigEndian.PutUint32(lenb[:], uint32(len(c.Entries)))
	payload.Write(lenb[:])
	for _, e := range c.Entries {
		payload.Write(encodeEntry(e))
	}
	checksum := sha256.Sum256(payload.Bytes())

	header := make([]byte, 0, headerSize)
	header = append(header, []byte(chunkMagic)...)
	header = append(header, c.Fingerprint[:]...)
	var seqb [8]byte
	binary.BigEndian.PutUint64(seqb[:], c.Seq)
	header = append(header, seqb[:]...)
	header = append(header, checksum[:]...)
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload.Bytes())
	return err
}
