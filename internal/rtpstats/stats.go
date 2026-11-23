// Package rtpstats carries the transport recovery statistics across package
// boundaries without coupling the validator to the RTP recoverer internals.
package rtpstats

// Stats is the recovery accounting for one stream, mirrored from the RTP
// recoverer so the render and validate layers consume a stable data type.
type Stats struct {
	Expected      int     `json:"expected"`
	Received      int     `json:"received"`
	InOrder       int     `json:"in_order"`
	Recovered     int     `json:"recovered"`
	Duplicates    int     `json:"duplicates"`
	Dropped       int     `json:"dropped"`
	Unrecoverable int     `json:"unrecoverable"`
	Malformed     int     `json:"malformed"`
	P95LatencyMs  float64 `json:"p95_latency_ms"`
}

// RecoveredRate is the fraction of expected unique packets that ended up
// available (in-order or recovered), never crediting duplicates.
func (s Stats) RecoveredRate() float64 {
	if s.Expected == 0 {
		return 0
	}
	return float64(s.InOrder+s.Recovered) / float64(s.Expected)
}
