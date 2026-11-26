package cmaf

import (
	"encoding/json"
	"os"
)

// LoadFragment parses a fragment file from disk into a Fragment plus the raw
// bytes, both needed by the repair stage (offsets) and the artifact writer.
func LoadFragment(path, source string) (*Fragment, []byte, error) {
	f, err := ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	frag, err := ParseFragment(f)
	if err != nil {
		return nil, nil, err
	}
	frag.Source = source
	return frag, raw, nil
}

// MarshalDeterministic encodes v without HTML escaping, so artifact bytes are
// identical across runs and platforms.
func MarshalDeterministic(v interface{}) ([]byte, error) {
	var buf = &jsonBuffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type jsonBuffer struct{ data []byte }

func (b *jsonBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *jsonBuffer) Bytes() []byte { return b.data }
