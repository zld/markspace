// Package journal provides recoverable delivery state and commit logging.
package journal

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"replayctl/internal/fault"
	"replayctl/internal/validate"
)

// LockName is the advisory lock file that serializes publish attempts.
const LockName = ".publish.lock"

// JournalName is the append-only commitment log inside an output directory.
const JournalName = "journal.jsonl"

// ManifestName is the verified delivery manifest read by `verify`.
const ManifestName = "manifest.json"

// Store roots same-volume temporary files, final artifacts, and the journal.
type Store struct {
	Dir         string
	DeliveryDir string
	JournalPath string
}

// Record is one committed delivery-key state.
type Record struct {
	Schema   string `json:"schema"`
	Key      string `json:"delivery_key"`
	EventID  string `json:"event_id"`
	StartMS  int64  `json:"start_ms"`
	EndMS    int64  `json:"end_ms"`
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
}

// Manifest is the verified state written after a journal commit.
type Manifest struct {
	Schema       string           `json:"schema"`
	DeliveryKey  string           `json:"delivery_key"`
	EventID      string           `json:"event_id"`
	StartMS      int64            `json:"start_ms"`
	EndMS        int64            `json:"end_ms"`
	Artifact     string           `json:"artifact"`
	SHA256       string           `json:"sha256"`
	Metrics      validate.Metrics `json:"metrics"`
	RecoveredRun bool             `json:"recovered_run,omitempty"`
}

// Open validates and creates the output and delivery directories.
func Open(outDir string) (*Store, error) {
	dir, err := ValidateOutputPath(outDir)
	if err != nil {
		return nil, err
	}
	deliveryDir := filepath.Join(dir, "deliveries")
	if err := os.MkdirAll(deliveryDir, 0o755); err != nil {
		return nil, fault.New(fault.Journal, fault.StageJournal, dir, -1, -1, "create deliveries: %v", err)
	}
	// Persist the newly-created "deliveries" directory entry in dir.
	if err := SyncDirectory(dir); err != nil {
		return nil, err
	}
	return &Store{Dir: dir, DeliveryDir: deliveryDir, JournalPath: filepath.Join(dir, JournalName)}, nil
}

// Lock serializes publish attempts against this output directory. It returns a
// release function; the lock is advisory and only guards cooperating replayctl
// processes. Locking before reading the journal ensures concurrent runs with the
// same delivery key observe each other's committed state instead of appending
// duplicate records.
func (s *Store) Lock() (func(), error) {
	path := filepath.Join(s.Dir, LockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fault.New(fault.Journal, fault.StageJournal, path, -1, -1, "open publish lock: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fault.New(fault.Journal, fault.StageJournal, path, -1, -1, "acquire publish lock: %v", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// ValidateOutputPath rejects empty, NUL-containing, or traversal-escaping roots.
func ValidateOutputPath(outDir string) (string, error) {
	if strings.TrimSpace(outDir) == "" {
		return "", fault.New(fault.Usage, fault.StageJournal, outDir, -1, -1, "missing --out directory")
	}
	if strings.ContainsRune(outDir, '\x00') {
		return "", fault.New(fault.Usage, fault.StageJournal, outDir, -1, -1, "NUL in output path")
	}
	abs, err := filepath.Abs(filepath.Clean(outDir))
	if err != nil {
		return "", fault.New(fault.Usage, fault.StageJournal, outDir, -1, -1, "invalid output path: %v", err)
	}
	return abs, nil
}

// DeliveryKey derives a stable key from event identity and half-open interval.
func DeliveryKey(eventID string, startMS, endMS int64) string {
	payload := fmt.Sprintf("replay-delivery-v1\nevent_id=%s\nstart_ms=%d\nend_ms=%d\n", eventID, startMS, endMS)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// ArtifactName returns the safe final MP4 basename for a key.
func ArtifactName(key string) string { return key + ".mp4" }

// FinalPath validates that the keyed artifact remains inside deliveries.
func (s *Store) FinalPath(key string) (string, error) {
	if len(key) != 64 {
		return "", fault.New(fault.Journal, fault.StageJournal, s.DeliveryDir, -1, -1, "invalid delivery key length")
	}
	for _, c := range key {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return "", fault.New(fault.Journal, fault.StageJournal, s.DeliveryDir, -1, -1, "invalid delivery key character")
		}
	}
	final := filepath.Join(s.DeliveryDir, ArtifactName(key))
	rel, err := filepath.Rel(s.DeliveryDir, final)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fault.New(fault.Journal, fault.StageJournal, s.DeliveryDir, -1, -1, "unsafe artifact path")
	}
	return final, nil
}

// CreateTemp creates a same-volume temporary artifact.
func (s *Store) CreateTemp(key string) (*os.File, error) {
	if _, err := s.FinalPath(key); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(s.DeliveryDir, ".tmp-"+key+"-*.mp4")
	if err != nil {
		return nil, fault.New(fault.IO, fault.StageJournal, s.DeliveryDir, -1, -1, "create temp artifact: %v", err)
	}
	return f, nil
}

// Load reads committed records keyed by delivery key.
func (s *Store) Load() (map[string]Record, error) {
	out := map[string]Record{}
	f, err := os.Open(s.JournalPath)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, fault.New(fault.Journal, fault.StageJournal, s.JournalPath, -1, -1, "open journal: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, fault.New(fault.Journal, fault.StageJournal, s.JournalPath, int64(lineNo), -1, "invalid journal line: %v", err)
		}
		out[rec.Key] = rec
	}
	if err := sc.Err(); err != nil {
		return nil, fault.New(fault.Journal, fault.StageJournal, s.JournalPath, int64(lineNo), -1, "read journal: %v", err)
	}
	return out, nil
}

// AppendRecord fsyncs one commitment record.
func (s *Store) AppendRecord(rec Record) error {
	if rec.Schema == "" {
		rec.Schema = "replay-journal-v1"
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fault.New(fault.Journal, fault.StageJournal, s.JournalPath, -1, -1, "encode record: %v", err)
	}
	_, statErr := os.Stat(s.JournalPath)
	if statErr != nil && !os.IsNotExist(statErr) {
		return fault.New(fault.Journal, fault.StageJournal, s.JournalPath, -1, -1, "stat journal: %v", statErr)
	}
	firstCreate := os.IsNotExist(statErr)
	line = append(line, '\n')
	f, err := os.OpenFile(s.JournalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fault.New(fault.Journal, fault.StageJournal, s.JournalPath, -1, -1, "append journal: %v", err)
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		return fault.New(fault.Journal, fault.StageJournal, s.JournalPath, -1, -1, "write journal: %v", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fault.New(fault.Journal, fault.StageJournal, s.JournalPath, -1, -1, "sync journal: %v", err)
	}
	if err := f.Close(); err != nil {
		return fault.New(fault.Journal, fault.StageJournal, s.JournalPath, -1, -1, "close journal: %v", err)
	}
	if firstCreate {
		return SyncDirectory(s.Dir)
	}
	return nil
}

// SyncDirectory fsyncs a directory after atomic rename.
func SyncDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fault.New(fault.IO, fault.StageJournal, dir, -1, -1, "open directory for sync: %v", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fault.New(fault.IO, fault.StageJournal, dir, -1, -1, "sync directory: %v", err)
	}
	return nil
}
