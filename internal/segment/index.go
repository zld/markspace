package segment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"

	"mediapipe/internal/model"
)

const IndexFormatVersion = 1

// Index is the recoverable local segment index persisted as MP4 metadata.
type Index struct {
	Version         int                   `json:"version"`
	InputSHA256     string                `json:"input_sha256"`
	ProcessedPacket int64                 `json:"processed_packet"`
	Complete        bool                  `json:"complete"`
	Segments        []model.Segment       `json:"segments"`
	Mappings        []model.DedupeMapping `json:"mappings,omitempty"`
}

// AtomicWriteFile writes data in the same directory and renames it into place.
func AtomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// WriteJSON atomically writes the JSON recovery sidecar.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return AtomicWriteFile(path, data)
}

func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return errors.New("empty JSON file")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse JSON %s: %w", path, err)
	}
	return nil
}

// WriteMP4Index stores the index JSON inside a standard ISO-BMFF free box.
func WriteMP4Index(path string, idx Index) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-index-*.mp4")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	box := &mp4.Free{Data: data}
	var payload bytes.Buffer
	if _, err := mp4.Marshal(&payload, box, mp4.Context{}); err != nil {
		cleanup()
		return err
	}
	boxInfo := &mp4.BoxInfo{
		Type:       mp4.BoxTypeFree(),
		Size:       uint64(mp4.SmallHeaderSize + payload.Len()),
		HeaderSize: mp4.SmallHeaderSize,
	}
	if _, err := mp4.WriteBoxInfo(tmp, boxInfo); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(payload.Bytes()); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// ReadMP4Index extracts and decodes the free-box JSON metadata.
func ReadMP4Index(path string) (Index, error) {
	var idx Index
	f, err := os.Open(path)
	if err != nil {
		return idx, err
	}
	defer f.Close()

	boxes, err := mp4.ExtractBoxWithPayload(f, nil, mp4.BoxPath{mp4.BoxTypeFree()})
	if err != nil {
		return idx, fmt.Errorf("read MP4 index boxes: %w", err)
	}
	if len(boxes) == 0 {
		return idx, errors.New("MP4 index contains no free metadata box")
	}
	free, ok := boxes[0].Payload.(*mp4.Free)
	if !ok {
		return idx, fmt.Errorf("index box payload type %T is not free", boxes[0].Payload)
	}
	if len(free.Data) == 0 {
		return idx, errors.New("MP4 index metadata is empty/incomplete")
	}
	if err := json.Unmarshal(free.Data, &idx); err != nil {
		return idx, fmt.Errorf("decode MP4 index JSON: %w", err)
	}
	if idx.Version != IndexFormatVersion {
		return idx, fmt.Errorf("unsupported index version %d", idx.Version)
	}
	return idx, nil
}

// IsIncompleteIndex reports whether an index should trigger checkpoint recovery.
func IsIncompleteIndex(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, os.ErrNotExist) || true
}
