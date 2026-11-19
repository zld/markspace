// Package fixture writes deterministic demo media so that the tool can be
// exercised end to end without any external assets.
package fixture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"streamcheck/internal/manifest"
	"streamcheck/internal/model"
)

// RateDurations is the deterministic per-segment duration sequence, in
// milliseconds. It sums to 12000ms over 12 segments, giving the demo its
// expected duration_ms of about 12000.
var RateDurations = []int64{1000, 1000, 900, 1100, 1000, 950, 1050, 1000, 1000, 1000, 1000, 1000}

// Plan describes one demo run.
type Plan struct {
	Root     string
	Rates    []int
	Segments int
	Drop     map[int]int  // rate -> number to omit
	Shuffle  map[int]bool // rate -> write manifest in shuffled order
}

// ParseDrops parses "1600000:5" style drop specifications.
func ParseDrops(values []string) (map[int]int, error) {
	out := make(map[int]int, len(values))
	for _, value := range values {
		rateText, numberText, ok := strings.Cut(value, ":")
		if !ok || rateText == "" || numberText == "" {
			return nil, fmt.Errorf("drop %q must be <rate>:<number>", value)
		}
		rate, ok := parseDigits(rateText)
		if !ok || rate <= 0 {
			return nil, fmt.Errorf("drop %q: invalid rate", value)
		}
		number, ok := parseDigits(numberText)
		if !ok {
			return nil, fmt.Errorf("drop %q: invalid number", value)
		}
		if _, exists := out[rate]; exists {
			return nil, fmt.Errorf("drop %q: rate %d listed twice", value, rate)
		}
		out[rate] = number
	}
	return out, nil
}

// ParseRates parses a comma-separated rate list such as
// "800000,1600000,3200000".
func ParseRates(text string) ([]int, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("rates are required")
	}
	parts := strings.Split(text, ",")
	rates := make([]int, 0, len(parts))
	seen := make(map[int]bool, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("rates contain an empty entry")
		}
		rate, ok := parseDigits(part)
		if !ok || rate <= 0 {
			return nil, fmt.Errorf("rate %q must be a positive integer", part)
		}
		if seen[rate] {
			return nil, fmt.Errorf("rate %d listed twice", rate)
		}
		seen[rate] = true
		rates = append(rates, rate)
	}
	sort.Ints(rates)
	return rates, nil
}

// parseDigits accepts a non-empty run of ASCII digits only; Sscanf would
// happily accept "5x" and silently ignore the tail.
func parseDigits(text string) (int, bool) {
	if text == "" {
		return 0, false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	value := 0
	for _, r := range text {
		value = value*10 + int(r-'0')
		if value > 1<<31 {
			return 0, false
		}
	}
	return value, true
}

// Generate writes segment files and one manifest per rate. It drops the
// configured segments, and for rates marked for shuffling writes the manifest
// entries in a rotated order so the verifier has an out-of-order case to
// find. All writes use the atomic writer.
func Generate(ctx context.Context, plan Plan) error {
	if plan.Segments <= 0 {
		return fmt.Errorf("segments must be positive")
	}
	if plan.Segments > model.MaxSegments {
		return fmt.Errorf("segments %d exceeds limit %d", plan.Segments, model.MaxSegments)
	}
	if len(plan.Rates) == 0 {
		return fmt.Errorf("at least one rate is required")
	}
	if err := os.MkdirAll(plan.Root, 0o755); err != nil {
		return fmt.Errorf("create root: %w", err)
	}
	for _, rate := range plan.Rates {
		if err := ctx.Err(); err != nil {
			return err
		}
		dropped, drop := plan.Drop[rate]
		segments := make([]model.Segment, 0, plan.Segments)
		for number := 0; number < plan.Segments; number++ {
			if drop && number == dropped {
				continue
			}
			duration := RateDurations[number%len(RateDurations)]
			// Content is derived from rate and number so that runs are
			// byte-identical, and every number inside a rate hashes
			// differently, which keeps checksum findings meaningful.
			body := fmt.Sprintf("STRMCHK rate=%d number=%d duration_ms=%d\n", rate, number, duration)
			name := fmt.Sprintf("seg_%d_%d_%d.ts", rate, number, duration)
			path := filepath.Join(plan.Root, name)
			if err := writeSmall(ctx, path, []byte(body)); err != nil {
				return err
			}
			segments = append(segments, model.Segment{
				Rate:       rate,
				Number:     number,
				DurationMS: duration,
				Path:       path,
			})
		}
		playlistOrder := append([]model.Segment(nil), segments...)
		if plan.Shuffle[rate] && len(playlistOrder) > 2 {
			// Rotate the first three entries; every number stays declared
			// exactly once, but the order now descends at the seam.
			playlistOrder[0], playlistOrder[2] = playlistOrder[2], playlistOrder[0]
		}
		data, err := manifest.Build(rate, playlistOrder)
		if err != nil {
			return fmt.Errorf("build manifest for rate %d: %w", rate, err)
		}
		if err := manifest.WriteFileAtomic(ctx, filepath.Join(plan.Root, fmt.Sprintf("%d.m3u8", rate)), data); err != nil {
			return err
		}
	}
	return nil
}

func writeSmall(ctx context.Context, path string, body []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return manifest.WriteFileAtomic(ctx, path, body)
}
