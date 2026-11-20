// Package report unifies terminal key=value output and JSONL emission.
package report

import (
	"encoding/json"
	"fmt"
	"io"
)

// JSONL writes one JSON object per line.
type JSONL struct {
	enc *json.Encoder
}

// NewJSONL wraps w for JSONL emission.
func NewJSONL(w io.Writer) *JSONL {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &JSONL{enc: enc}
}

// Emit writes v as one JSON line.
func (j *JSONL) Emit(v any) error {
	return j.enc.Encode(v)
}

// KV prints alternating key/value pairs as key=value tokens on one line.
func KV(w io.Writer, kv ...any) error {
	if len(kv)%2 != 0 {
		return fmt.Errorf("report.KV: odd argument count %d", len(kv))
	}
	for i := 0; i < len(kv); i += 2 {
		if i > 0 {
			if _, err := fmt.Fprint(w, " "); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%v=%v", kv[i], kv[i+1]); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}
