package rtpreplay

import (
	"bytes"
	"encoding/json"
)

// marshalCanonical renders v with no HTML escaping and no trailing newline so
// digests are stable across writers.
func marshalCanonical(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, NewError(CodeInvalidInput, "marshal", err.Error()).WithCause(err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
