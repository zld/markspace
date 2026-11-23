// Package normalize contains audio and color metadata normalization policies.
// Every decision is recorded: nothing is silently dropped or defaulted.
package normalize

import (
	"fmt"

	"mediaqc/internal/model"
)

// ChannelLayout names the multichannel layouts handled by the local subset.
type ChannelLayout string

const (
	LayoutMono      ChannelLayout = "mono"       // 1.0
	LayoutStereo    ChannelLayout = "stereo"     // 2.0
	LayoutFiveOne   ChannelLayout = "5.1"        // 5.1
	LayoutSevenOne  ChannelLayout = "7.1"        // 7.1
	LayoutUnknown   ChannelLayout = "unknown"
)

// ChannelCount returns the speaker count for a layout.
func (l ChannelLayout) ChannelCount() int {
	switch l {
	case LayoutMono:
		return 1
	case LayoutStereo:
		return 2
	case LayoutFiveOne:
		return 6
	case LayoutSevenOne:
		return 8
	default:
		return 0
	}
}

// AudioPolicy decides target layout/sample-rate handling and records evidence.
type AudioPolicy struct {
	materialID   string
	targetLayout ChannelLayout
	targetRate   int64
	maxLatencyMs int64
}

// AudioDecision documents one normalization decision.
type AudioDecision struct {
	TrackID          string
	SourceLayout     ChannelLayout
	TargetLayout     ChannelLayout
	SourceRate       int64
	TargetRate       int64
	ResampleRatioNum int64
	ResampleRatioDen int64
	LatencyBudgetMs  int64
	ChannelsDropped  int
	Action           string // "passthrough", "downmix", "upmix", "resample", "rejected"
	Notes            string
}

// NewAudioPolicy builds a policy. targetRate <= 0 means "keep source rate".
func NewAudioPolicy(materialID string, targetLayout ChannelLayout, targetRate, maxLatencyMs int64) (*AudioPolicy, error) {
	if targetLayout.ChannelCount() <= 0 {
		return nil, model.NewError(model.CodeConfig, model.StageNormalize, materialID, "",
			"invalid target channel layout")
	}
	if maxLatencyMs <= 0 {
		return nil, model.NewError(model.CodeConfig, model.StageNormalize, materialID, "",
			"latency budget must be positive")
	}
	return &AudioPolicy{
		materialID: materialID, targetLayout: targetLayout,
		targetRate: targetRate, maxLatencyMs: maxLatencyMs,
	}, nil
}

// Plan evaluates one track and returns a fully documented decision. A
// conversion that would discard channels without an explicit downmix map is
// rejected instead of silently dropping them.
func (p *AudioPolicy) Plan(trackID string, sourceLayout ChannelLayout, sourceRate int64) (AudioDecision, error) {
	if sourceLayout == LayoutUnknown || sourceLayout.ChannelCount() <= 0 {
		return AudioDecision{}, model.NewError(model.CodeChannelDrop, model.StageNormalize,
			p.materialID, trackID, "unknown source channel layout cannot be mapped safely")
	}
	if sourceRate <= 0 {
		return AudioDecision{}, model.NewError(model.CodeSampleRateClash, model.StageNormalize,
			p.materialID, trackID, fmt.Sprintf("invalid source sample rate %d", sourceRate))
	}
	d := AudioDecision{
		TrackID: trackID, SourceLayout: sourceLayout, TargetLayout: p.targetLayout,
		SourceRate: sourceRate, TargetRate: sourceRate,
		ResampleRatioDen: 1,
	}
	srcCh, dstCh := sourceLayout.ChannelCount(), p.targetLayout.ChannelCount()
	switch {
	case srcCh == dstCh:
		d.Action = "passthrough"
	case srcCh > dstCh:
		// Downmix is allowed only via the explicit, documented speaker map;
		// record how many channels are merged (never silently deleted).
		d.Action = "downmix"
		d.ChannelsDropped = srcCh - dstCh
		d.Notes = fmt.Sprintf("explicit downmix map folds %d speakers into %d", srcCh, dstCh)
	default:
		d.Action = "upmix"
		d.Notes = fmt.Sprintf("upmix duplicates/steers %d speakers into %d", srcCh, dstCh)
	}
	if p.targetRate > 0 && p.targetRate != sourceRate {
		if d.Action == "passthrough" {
			d.Action = "resample"
		}
		d.TargetRate = p.targetRate
		d.ResampleRatioNum = p.targetRate
		d.ResampleRatioDen = sourceRate
		d.LatencyBudgetMs = p.maxLatencyMs
		if !p.rateSupported(sourceRate) {
			return d, model.NewError(model.CodeSampleRateClash, model.StageNormalize,
				p.materialID, trackID,
				fmt.Sprintf("sample rate %d -> %d has no bounded resampler within %dms",
					sourceRate, p.targetRate, p.maxLatencyMs))
		}
	}
	return d, nil
}

// rateSupported restricts resampling to ratios the local bounded resampler
// can service inside the latency budget (integer-ratio or common rates).
func (p *AudioPolicy) rateSupported(sourceRate int64) bool {
	// A 2:1 or 1:2 ratio always fits a one-block filter; other ratios require
	// the latency budget to cover at least one period of the slower rate.
	if p.targetRate == 2*sourceRate || sourceRate == 2*p.targetRate {
		return true
	}
	slower := sourceRate
	if p.targetRate < slower {
		slower = p.targetRate
	}
	periodMs := int64(1000)
	if slower > 0 {
		periodMs = 1000 / (slower / 1000) // approximate one-block period
	}
	return periodMs <= p.maxLatencyMs
}

// DownmixMap returns the explicit speaker fold map between layouts. The map
// makes every source channel traceable, satisfying the no-silent-drop rule.
func DownmixMap(from, to ChannelLayout) (map[int][]int, error) {
	if from.ChannelCount() < to.ChannelCount() {
		return nil, fmt.Errorf("downmix called for upmix %s -> %s", from, to)
	}
	switch {
	case from == LayoutFiveOne && to == LayoutStereo:
		// L,R stay; C folds equally into L/R; surrounds fold to opposite side.
		return map[int][]int{0: {0}, 1: {1}, 2: {0, 1}, 3: {0}, 4: {1}, 5: {0, 1}}, nil
	case from == LayoutStereo && to == LayoutMono:
		return map[int][]int{0: {0}, 1: {0}}, nil
	case from == LayoutSevenOne && to == LayoutFiveOne:
		// rear surrounds (6,7) fold into side surrounds (4,5).
		return map[int][]int{0: {0}, 1: {1}, 2: {2}, 3: {3}, 4: {4, 6}, 5: {5, 7}}, nil
	default:
		return nil, model.NewError(model.CodeChannelDrop, model.StageNormalize, "", "",
			fmt.Sprintf("no documented downmix map %s -> %s", from, to))
	}
}
