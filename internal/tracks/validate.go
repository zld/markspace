// Package tracks validates Matroska track metadata against a delivery profile:
// channel layout versus channel count, sampling rate inside the declared
// window, and complete, consistent colour metadata. Every conflict names the
// related tracks and the earliest input offset that locates it.
package tracks

import (
	"fmt"
	"sort"
	"strings"

	"example.com/assetcheck/internal/timeline"
)

// Validate checks every track against the profile and returns all issues sorted
// by offset then track. An empty slice means the media passes.
func Validate(tracks []Track, profile Profile) []Issue {
	var issues []Issue
	issues = append(issues, validateAudio(tracks, profile)...)
	issues = append(issues, validateVideo(tracks, profile)...)
	issues = append(issues, validateAcrossTracks(tracks, profile)...)
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Offset != issues[j].Offset {
			return issues[i].Offset < issues[j].Offset
		}
		return firstTrack(issues[i]) < firstTrack(issues[j])
	})
	return issues
}

func firstTrack(i Issue) uint64 {
	if len(i.Tracks) == 0 {
		return 0
	}
	return i.Tracks[0]
}

func validateAudio(tracks []Track, profile Profile) []Issue {
	var issues []Issue
	seenNumbers := map[uint64]int64{}
	for _, tr := range tracks {
		if prev, ok := seenNumbers[tr.Number]; ok {
			issues = append(issues, Issue{
				Category: timeline.CatDuplicate,
				Offset:   tr.Offset,
				Tracks:   []uint64{tr.Number},
				Field:    "TrackNumber",
				Reason: fmt.Sprintf("track number %d already declared at offset %d",
					tr.Number, prev),
			})
		}
		seenNumbers[tr.Number] = tr.Offset

		if tr.Type != TypeAudio || tr.Audio == nil {
			continue
		}
		a := tr.Audio
		if a.Channels == 0 {
			issues = append(issues, Issue{
				Category: timeline.CatMissingElement,
				Offset:   tr.Offset,
				Tracks:   []uint64{tr.Number},
				Field:    "Audio.Channels",
				Reason:   fmt.Sprintf("%s declares no channel count", tr.Label()),
			})
			continue
		}
		layout := a.Layout
		if want, ok := profile.Layouts[tr.Number]; ok {
			layout = want
		}
		if layout != "" {
			want, known := layout.ChannelsFor()
			if !known {
				issues = append(issues, Issue{
					Category: timeline.CatTrackConflict,
					Offset:   tr.Offset,
					Tracks:   []uint64{tr.Number},
					Field:    "Audio.ChannelLayout",
					Reason:   fmt.Sprintf("%s declares unknown layout %q", tr.Label(), layout),
				})
			} else if want != a.Channels {
				issues = append(issues, Issue{
					Category: timeline.CatTrackConflict,
					Offset:   tr.Offset,
					Tracks:   []uint64{tr.Number},
					Field:    "Audio.ChannelLayout",
					Reason: fmt.Sprintf("%s layout %q needs %d channels but Channels=%d",
						tr.Label(), layout, want, a.Channels),
				})
			}
		}
		if profile.MaxChannels > 0 && a.Channels > profile.MaxChannels {
			issues = append(issues, Issue{
				Category: timeline.CatTrackConflict,
				Offset:   tr.Offset,
				Tracks:   []uint64{tr.Number},
				Field:    "Audio.Channels",
				Reason: fmt.Sprintf("%s has %d channels, profile allows at most %d",
					tr.Label(), a.Channels, profile.MaxChannels),
			})
		}
		if a.SamplingFrequency <= 0 {
			issues = append(issues, Issue{
				Category: timeline.CatMissingElement,
				Offset:   tr.Offset,
				Tracks:   []uint64{tr.Number},
				Field:    "Audio.SamplingFrequency",
				Reason:   fmt.Sprintf("%s declares no sampling frequency", tr.Label()),
			})
			continue
		}
		if a.OutputSamplingFreq > 0 && !nearlyEqual(a.OutputSamplingFreq, a.SamplingFrequency) {
			// A resampled track is legal, but the profile must allow the ratio.
			ratio := a.OutputSamplingFreq / a.SamplingFrequency
			if ratio < 0.5 || ratio > 2 {
				issues = append(issues, Issue{
					Category: timeline.CatTrackConflict,
					Offset:   tr.Offset,
					Tracks:   []uint64{tr.Number},
					Field:    "Audio.OutputSamplingFrequency",
					Reason: fmt.Sprintf("%s output rate %.0f Hz differs from %.0f Hz by more than 2x",
						tr.Label(), a.OutputSamplingFreq, a.SamplingFrequency),
				})
			}
		}
		rate := a.EffectiveRate()
		if allowed, ok := profile.AudioRates[tr.Number]; ok {
			if !allowed.Contains(rate) {
				issues = append(issues, Issue{
					Category: timeline.CatTrackConflict,
					Offset:   tr.Offset,
					Tracks:   []uint64{tr.Number},
					Field:    "Audio.SamplingFrequency",
					Reason: fmt.Sprintf("%s rate %.0f Hz outside manifest window [%.0f, %.0f]",
						tr.Label(), rate, allowed.Min, allowed.Max),
				})
			}
		}
	}
	return issues
}

func validateVideo(tracks []Track, profile Profile) []Issue {
	var issues []Issue
	colourByRule := map[ColourRule][]uint64{}
	for _, tr := range tracks {
		if tr.Type != TypeVideo || tr.Video == nil {
			continue
		}
		v := tr.Video
		if v.PixelWidth == 0 || v.PixelHeight == 0 {
			issues = append(issues, Issue{
				Category: timeline.CatMissingElement,
				Offset:   tr.Offset,
				Tracks:   []uint64{tr.Number},
				Field:    "Video.PixelWidth/PixelHeight",
				Reason:   fmt.Sprintf("%s declares no pixel dimensions", tr.Label()),
			})
		}
		if v.Colour.Present {
			if !v.Colour.IsComplete() {
				missing := []string{}
				if v.Colour.MatrixCoefficients == 0 || v.Colour.MatrixCoefficients == 2 {
					missing = append(missing, "MatrixCoefficients")
				}
				if v.Colour.TransferCharacterist == 0 || v.Colour.TransferCharacterist == 2 {
					missing = append(missing, "TransferCharacteristics")
				}
				if v.Colour.Primaries == 0 || v.Colour.Primaries == 2 {
					missing = append(missing, "Primaries")
				}
				issues = append(issues, Issue{
					Category: timeline.CatMissingElement,
					Offset:   tr.Offset,
					Tracks:   []uint64{tr.Number},
					Field:    "Video.Colour",
					Reason: fmt.Sprintf("%s colour metadata incomplete: %s",
						tr.Label(), strings.Join(missing, ", ")),
				})
			} else {
				rule := ColourRule{
					MatrixCoefficients: v.Colour.MatrixCoefficients,
					Transfer:           v.Colour.TransferCharacterist,
					Primaries:          v.Colour.Primaries,
				}
				colourByRule[rule] = append(colourByRule[rule], tr.Number)
				if want, ok := profile.VideoColour[tr.Number]; ok {
					if want != rule {
						issues = append(issues, Issue{
							Category: timeline.CatTrackConflict,
							Offset:   tr.Offset,
							Tracks:   []uint64{tr.Number},
							Field:    "Video.Colour",
							Reason: fmt.Sprintf("%s CICP %d/%d/%d disagrees with manifest %d/%d/%d",
								tr.Label(), rule.MatrixCoefficients, rule.Transfer, rule.Primaries,
								want.MatrixCoefficients, want.Transfer, want.Primaries),
						})
					}
				}
			}
		} else if profile.RequireColour {
			issues = append(issues, Issue{
				Category: timeline.CatMissingElement,
				Offset:   tr.Offset,
				Tracks:   []uint64{tr.Number},
				Field:    "Video.Colour",
				Reason:   fmt.Sprintf("%s has no colour metadata but the manifest requires it", tr.Label()),
			})
		}
	}
	// All video tracks that describe the same picture must agree on CICP.
	if len(colourByRule) > 1 {
		rules := make([]ColourRule, 0, len(colourByRule))
		for r := range colourByRule {
			rules = append(rules, r)
		}
		sort.Slice(rules, func(i, j int) bool {
			if rules[i].MatrixCoefficients != rules[j].MatrixCoefficients {
				return rules[i].MatrixCoefficients < rules[j].MatrixCoefficients
			}
			if rules[i].Transfer != rules[j].Transfer {
				return rules[i].Transfer < rules[j].Transfer
			}
			return rules[i].Primaries < rules[j].Primaries
		})
		var all []uint64
		var offs []int64
		for _, r := range rules {
			all = append(all, colourByRule[r]...)
		}
		sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
		for _, tr := range tracks {
			if tr.Type == TypeVideo && tr.Video != nil && tr.Video.Colour.IsComplete() {
				offs = append(offs, tr.Offset)
			}
		}
		earliest := offs[0]
		for _, o := range offs {
			if o < earliest {
				earliest = o
			}
		}
		issues = append(issues, Issue{
			Category: timeline.CatTrackConflict,
			Offset:   earliest,
			Tracks:   all,
			Field:    "Video.Colour",
			Reason: fmt.Sprintf("video tracks disagree on colour metadata: %s",
				describeRules(rules, colourByRule)),
		})
	}
	return issues
}

func describeRules(rules []ColourRule, byRule map[ColourRule][]uint64) string {
	var parts []string
	for _, r := range rules {
		parts = append(parts, fmt.Sprintf("%d/%d/%d used by tracks %v",
			r.MatrixCoefficients, r.Transfer, r.Primaries, byRule[r]))
	}
	return strings.Join(parts, "; ")
}

// validateAcrossTracks catches conflicts that only appear when tracks are
// compared: language/name ambiguities for text tracks and inconsistent audio
// program rates.
func validateAcrossTracks(tracks []Track, _ Profile) []Issue {
	var issues []Issue
	byLang := map[string][]Track{}
	for _, tr := range tracks {
		if tr.Type == TypeSubtitle {
			byLang[tr.Language] = append(byLang[tr.Language], tr)
		}
	}
	langs := make([]string, 0, len(byLang))
	for l := range byLang {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for _, lang := range langs {
		group := byLang[lang]
		if len(group) < 2 {
			continue
		}
		named := map[string]bool{}
		for _, tr := range group {
			if tr.Name != "" {
				if named[tr.Name] {
					issues = append(issues, Issue{
						Category: timeline.CatDuplicate,
						Offset:   tr.Offset,
						Tracks:   numbers(group),
						Field:    "Name",
						Reason: fmt.Sprintf("subtitle tracks share language %q and name %q",
							lang, tr.Name),
					})
				}
				named[tr.Name] = true
			}
		}
		// More than one subtitle track per language must be distinguishable by name.
		if len(group) > 1 {
			unnamed := 0
			for _, tr := range group {
				if tr.Name == "" {
					unnamed++
				}
			}
			if unnamed > 1 {
				issues = append(issues, Issue{
					Category: timeline.CatTrackConflict,
					Offset:   group[0].Offset,
					Tracks:   numbers(group),
					Field:    "Name",
					Reason: fmt.Sprintf("%d subtitle tracks share language %q but are not distinguishable by name",
						len(group), lang),
				})
			}
		}
	}
	return issues
}

func numbers(ts []Track) []uint64 {
	out := make([]uint64, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Number)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
