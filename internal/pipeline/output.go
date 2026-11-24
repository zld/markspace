package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"medpipe/internal/adapter/mp4v1"
	"medpipe/internal/clock"
	"medpipe/internal/media"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeManifestFile(path string, m Manifest) (string, []byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", nil, err
	}
	b = append(b, '\n')
	if err := clock.AtomicWriteFile(path, b, 0o644); err != nil {
		return "", nil, err
	}
	return sha256Hex(b), b, nil
}

func writeChecksumsFile(path string, segments []media.Segment, manifestSHA string) (string, error) {
	var b strings.Builder
	for _, seg := range segments {
		if seg.DuplicateOf != 0 || seg.File == "" || seg.SHA256 == "" {
			continue
		}
		fmt.Fprintf(&b, "%s  %s\n", seg.SHA256, filepath.ToSlash(seg.File))
	}
	fmt.Fprintf(&b, "%s  %s\n", manifestSHA, ManifestName)
	data := []byte(b.String())
	if err := clock.AtomicWriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

type segmentPayloadFile struct {
	Format  string        `json:"format"`
	Index   int           `json:"index"`
	StartNS int64         `json:"start_ns"`
	EndNS   int64         `json:"end_ns"`
	Frames  []media.Frame `json:"frames"`
}

func encodeSegmentPayload(built *media.BuiltSegment) ([]byte, error) {
	payload := segmentPayloadFile{
		Format: "medpipe-frames/v1", Index: built.Info.Index,
		StartNS: built.Info.StartNS, EndNS: built.Info.EndNS, Frames: built.Frames,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func verifySegmentMP4(data []byte, index int) error {
	loc := &clock.Location{ParserMajor: mp4v1.MajorVersion}
	doc, err := mp4v1.Decoder{}.Decode(bytes.NewReader(data), loc)
	if err != nil {
		return err
	}
	if doc.ParserMajor != mp4v1.MajorVersion || len(doc.Boxes) < 2 {
		return clock.Errorf(clock.CodeMissingField, loc, "MP4 adapter returned incomplete document")
	}
	return nil
}
