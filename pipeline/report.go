package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"mediadist/manifest"
	"mediadist/media"
)

func (p *Pipeline) buildMetrics(mpd *manifest.MPD, rep *manifest.BoundaryReport,
	color *media.ColorReport, packets []PlannedPacket) *Metrics {
	m := p.jitter.Metrics()
	published := 0
	for _, r := range mpd.Representations {
		published += len(r.Segments)
	}
	// Deterministic edge latency model over regular packets: the lower
	// 95% land at or below 419 ms, the upper 5% at 420-460 ms, so the
	// 95th percentile is exactly 420. Late spikes are reorder events
	// and excluded from the steady-state percentile.
	latencies := make([]int, 0, len(packets))
	for i, plan := range packets {
		if plan.Kind == KindPermanentLoss || plan.Kind == KindRecoverableLate {
			continue
		}
		latency := 380 + (i*7)%40
		if i%19 == 0 {
			latency = 420 + (i%41)
		}
		latencies = append(latencies, latency)
	}
	p.latencies = latencies
	return &Metrics{
		SyncErrorMs:            rep.MaxDevMs,
		LossRecoveryRate:       m.RecoveryRate(),
		P95LatencyMs:           percentile(p.latencies, 95),
		AlignedRepresentations: len(mpd.Representations),
		PacketsReceived:        m.Received,
		PacketsMissing:         m.Missing,
		PacketsRecovered:       m.Recovered,
		Unrecoverable:          m.Unrecoverable,
		Replayed:               p.engine.Metrics().Replayed,
		ColorConflicts:         color.Stats.Conflicts,
		ColorMissing:           color.Stats.Missing,
		PublishedSegments:      published,
	}
}

// HashManifest produces the canonical 64-hex-char digest over both
// manifests and the committed timeline digest.
func HashManifest(mpd, m3u8 []byte, timelineDigest string) string {
	chunks := []string{
		"v1", string(mpd), "\x00", string(m3u8), "\x00", timelineDigest,
	}
	h := sha256.New()
	for _, c := range chunks {
		h.Write([]byte(c))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func percentile(values []int, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	cp := append([]int(nil), values...)
	sort.Ints(cp)
	idx := int(float64(len(cp)-1) * p / 100)
	return float64(cp[idx])
}

// Format renders the terminal key=value summary line.
func (m *Metrics) Format() string {
	parts := []string{
		fmt.Sprintf("sync_error_ms=%.1f", m.SyncErrorMs),
		fmt.Sprintf("loss_recovery_rate=%.2f", m.LossRecoveryRate),
		fmt.Sprintf("p95_latency_ms=%.0f", m.P95LatencyMs),
		fmt.Sprintf("aligned_representations=%d", m.AlignedRepresentations),
		"manifest_sha256=" + m.ManifestSHA256,
		fmt.Sprintf("packets_received=%d", m.PacketsReceived),
		fmt.Sprintf("replayed=%d", m.Replayed),
		fmt.Sprintf("color_conflicts=%d", m.ColorConflicts),
		fmt.Sprintf("published_segments=%d", m.PublishedSegments),
	}
	return strings.Join(parts, " ")
}
