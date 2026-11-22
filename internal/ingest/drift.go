package ingest

import (
	"fmt"
	"strconv"
	"strings"

	"mediad/internal/clock"
	"mediad/internal/core"
	"mediad/internal/diagnose"
)

// DriftSampleRecord is one node-vs-source observation with its input span.
type DriftSampleRecord struct {
	Source core.Rational `json:"source"`
	Node   core.Rational `json:"node"`
	Span   core.Span     `json:"span"`
}

// DriftAsset is parsed drift evidence for one node.
type DriftAsset struct {
	Asset   string              `json:"asset"`
	Node    string              `json:"node"`
	Samples []DriftSampleRecord `json:"samples"`
	Model   *clock.DriftModel   `json:"model,omitempty"`
	Issues  diagnose.Issues     `json:"-"`
}

// DriftThresholdPPB is the correction magnitude considered reportable. A fit
// below it is still applied but is not treated as damage.
const DriftThresholdPPB = 500

// ParseDrift reads "#node NAME" plus "source_ms,node_ms" lines. A malformed
// line is reported at its byte span and skipped without losing later samples.
func ParseDrift(asset *Asset) (*DriftAsset, error) {
	da := &DriftAsset{Asset: asset.Name, Node: "unknown"}
	body, shift, _, badBOM := stripBOM(asset.Data)
	if badBOM != nil {
		da.Issues.Add(diagnose.Issue{
			Category: diagnose.CatUnsupported, Stage: diagnose.StageIngest,
			Format: "drift", Asset: asset.Name, Span: badBOM.span,
			Detail: "UTF-16 drift evidence requires UTF-8",
		})
		return da, nil
	}
	for _, ln := range splitLines(body) {
		text := strings.TrimSpace(ln.Text)
		start, end := ln.Start+shift, ln.End+shift
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "#node") {
			name := strings.TrimSpace(strings.TrimPrefix(text, "#node"))
			if name == "" {
				da.Issues.Add(diagnose.Issue{
					Category: diagnose.CatNodeDrift, Stage: diagnose.StageIngest,
					Format: "drift", Asset: asset.Name, Span: core.Span{Start: start, End: end},
					Detail: "empty node name",
				})
				continue
			}
			da.Node = name
			continue
		}
		if strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, ",")
		if len(fields) != 2 {
			da.Issues.Add(badDriftSample(asset.Name, start, end, "need source_ms,node_ms"))
			continue
		}
		src, err1 := parseInt64Field(fields[0])
		node, err2 := parseInt64Field(fields[1])
		if err1 != nil || err2 != nil {
			da.Issues.Add(badDriftSample(asset.Name, start, end, "non-integer milliseconds"))
			continue
		}
		da.Samples = append(da.Samples, DriftSampleRecord{
			Source: core.FromMillis(src),
			Node:   core.FromMillis(node),
			Span:   core.Span{Start: start, End: end},
		})
	}
	if len(da.Samples) >= 2 {
		samples := make([]clock.DriftSample, 0, len(da.Samples))
		for _, s := range da.Samples {
			samples = append(samples, clock.DriftSample{Source: s.Source, Node: s.Node})
		}
		model, err := clock.FitDrift(da.Node, samples)
		if err != nil {
			da.Issues.Add(badDriftSample(asset.Name, da.Samples[0].Span.Start, da.Samples[len(da.Samples)-1].Span.End, err.Error()))
		} else {
			model.Applied = true
			da.Model = &model
			if absInt64(model.RatePPB) > DriftThresholdPPB {
				first := da.Samples[0].Span
				last := da.Samples[len(da.Samples)-1].Span
				da.Issues.Add(diagnose.Issue{
					Category: diagnose.CatNodeDrift, Stage: diagnose.StageIngest,
					Format: "drift", Asset: asset.Name,
					Span: core.Span{Start: first.Start, End: last.End},
					Detail: fmt.Sprintf("node %s drifts %+d ppb (slope %s, intercept %s, max residual %s); correction applied",
						model.Node, model.RatePPB, model.Slope, model.Intercept, model.MaxResidual),
					Recoverable: true, Node: da.Node,
				})
			}
		}
	}
	return da, nil
}

func badDriftSample(asset string, start, end int64, detail string) diagnose.Issue {
	return diagnose.Issue{
		Category: diagnose.CatNodeDrift, Stage: diagnose.StageIngest,
		Format: "drift", Asset: asset, Span: core.Span{Start: start, End: end},
		Detail: detail, Recoverable: true,
	}
}

func parseInt64Field(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	return strconv.ParseInt(s, 10, 64)
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
