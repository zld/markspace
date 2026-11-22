package pipeline

import (
	"fmt"
	"strings"
)

// Format renders the report as deterministic key=value lines. Segment
// hashes are printed as the leading 64 bits of SHA-256.
func (r *Report) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "scenario=%s packets=%d loss=%d reordered=%d duplicates=%d late=%d "+
		"parse_rate=%.3f keyframe_hit_rate=%.3f damage_ranges=%d roundtrip_rate=%.3f\n",
		r.Scenario, r.Packets, r.Loss, r.Reordered, r.Duplicates, r.Late,
		r.ParseRate(), r.KeyframeHitRate, len(r.Damage), r.RoundtripRate)
	for i, h := range r.Segments {
		fmt.Fprintf(&b, "segment_sha256[%d]=%x\n", i, h[:8])
	}
	for i, d := range r.Damage {
		fmt.Fprintf(&b, "damage[%d]=class=%s bytes=[%d,%d) ticks=[%d,%d) recoverable=%t detail=%q\n",
			i, d.Class, d.ByteStart, d.ByteEnd, d.TickStart, d.TickEnd, d.Recoverable(), d.Detail)
	}
	if r.HasBoundary {
		fmt.Fprintf(&b, "recoverable_boundary=%d\n", r.RecoverableBoundary)
	}
	return b.String()
}
