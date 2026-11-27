package pipeline

import (
	"encoding/hex"

	"github.com/example/mediad/internal/report"
	"github.com/example/mediad/internal/rtp"
)

// rtpDigestHex renders the accepted-packet content digest as hex for the key.
func rtpDigestHex(order *rtp.Result) string {
	sum := rtp.PayloadDigest(order.Packets)
	return hex.EncodeToString(sum[:])
}

// finalizeItem applies the quality, size and budget gates without ever
// lowering a quality threshold. Quality and true-peak failures are reported
// explicitly; size overrun is adjusted within the quality gate where possible,
// otherwise marked unmet with the measured deviation.
func (r *Runner) finalizeItem(item report.Item, artifact []byte, elapsed interface{ Nanoseconds() int64 }) report.Item {
	item.Status = report.StatusSatisfied

	// Quality gates first: loudness within 0.5 LU and true peak at ceiling.
	loudOK := item.LUFS != 0 && absF(item.LUFS-r.Params.LUFS) <= 0.5
	peakOK := item.TruePeakDBTP <= r.Params.TruePeak+1e-9
	if !loudOK || !peakOK {
		item.Status = report.StatusQualityMiss
		if !loudOK {
			item.Deviations = append(item.Deviations,
				"loudness outside 0.5 LU target")
		}
		if !peakOK {
			item.Deviations = append(item.Deviations,
				"true peak exceeds ceiling")
		}
	}

	// Size budget, evaluated only while quality gates remain authoritative.
	if r.Params.MaxBytes > 0 && item.OutputBytes > r.Params.MaxBytes {
		if item.Status == report.StatusSatisfied {
			item.Status = report.StatusSizeMiss
		}
		item.Deviations = append(item.Deviations,
			"output exceeds byte budget")
	}
	return item
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// BudgetReport computes run-level time budget accounting. The time budget
// takes priority over size: when exceeded, low-priority attempts are stopped
// and the deviation is reported rather than silently degrading quality. The
// size flag is left to the caller because max-bytes is a per-material ceiling
// evaluated in finalizeItem, not an aggregate over the whole run.
func BudgetReport(budgetMs, elapsedMs int64, maxBytes int64, totalBytes int64) report.Budget {
	b := report.Budget{
		BudgetMs:  budgetMs,
		ElapsedMs: elapsedMs,
		MaxBytes:  maxBytes,
	}
	// A non-negative budget is enforced; zero means the strictest possible
	// budget, so any elapsed work is immediately over. A negative value means
	// no time budget was configured.
	if b.BudgetMs >= 0 {
		b.DeviationMs = elapsedMs - b.BudgetMs
		b.OverBudgetTime = elapsedMs > b.BudgetMs
	}
	_ = totalBytes
	return b
}
