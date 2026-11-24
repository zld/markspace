package container

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/zld/mediarc/internal/model"
)

// ReadPath enforces the input-size limit before reading the file.
func ReadPath(path string, cfg model.Config) (*File, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, &model.IntegrityError{Path: path, Err: err}
	}
	if st.Size() > cfg.Limits.MaxInputBytes {
		return nil, &model.ParseError{
			BoxType:    "file",
			ByteOffset: 0,
			Err:        fmt.Errorf("input size %d exceeds limit %d", st.Size(), cfg.Limits.MaxInputBytes),
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &model.IntegrityError{Path: path, Err: err}
	}
	f, err := ReadBytes(data, cfg)
	if err != nil {
		return nil, err
	}
	f.Path = path
	return f, nil
}

// ReadBytes parses an in-memory MP4/MOV asset.
func ReadBytes(data []byte, cfg model.Config) (*File, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if int64(len(data)) > cfg.Limits.MaxInputBytes {
		return nil, &model.ParseError{BoxType: "file", ByteOffset: 0, Err: errors.New("input exceeds size limit")}
	}
	digest := sha256.Sum256(data)
	f := &File{Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
	r := bytes.NewReader(data)
	if err := validateAndParseBoxes(r, int64(len(data)), cfg, f); err != nil {
		return nil, err
	}
	if err := f.assembleTracks(cfg); err != nil {
		return nil, err
	}
	arrival := 0
	for _, tr := range f.Tracks {
		for i := range tr.Events {
			tr.Events[i].ArrivalIndex = arrival
			arrival++
		}
	}
	return f, nil
}
