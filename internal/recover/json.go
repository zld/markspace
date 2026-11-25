package recover

import "encoding/json"

// jsonUnmarshal is a thin wrapper kept here so resume.go reads without a bare
// encoding/json import among the domain-level dependencies.
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
