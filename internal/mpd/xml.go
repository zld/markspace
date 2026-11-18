package mpd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Marshal renders the document as deterministic XML: a fixed XML declaration,
// namespace on the root element and attributes in struct-declaration order.
// encoding/xml escapes text and attribute values itself, including quotes and
// angle brackets, so no hand-rolled escaping is required.
func Marshal(doc *Document) ([]byte, error) {
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode MPD: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.Write(body)
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// WriteFile atomically writes the document to path: data is first written to a
// temp file in the destination directory, synced and renamed over the target.
// A failure at any stage leaves any pre-existing target file untouched.
func WriteFile(path string, doc *Document) (err error) {
	data, err := Marshal(doc)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".dashforge-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// WriteTo writes the rendered XML to w.
func WriteTo(w io.Writer, doc *Document) error {
	data, err := Marshal(doc)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write MPD: %w", err)
	}
	return nil
}
