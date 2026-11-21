package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"streamcheck/internal/build"
	"streamcheck/internal/fixture"
)

// demoArgs holds the parsed "demo" subcommand arguments.
type demoArgs struct {
	out        string
	duration   time.Duration
	segment    time.Duration
	tiers      []fixture.Tier
	faults     map[string]build.TierFault
	jsonOutput bool
}

// parseTiers converts "450k,900k,1800k" into ordered Tier descriptors. A
// suffix of k/m multiplies the numeric prefix by 1e3/1e6 bits per second.
func parseTiers(spec string) ([]fixture.Tier, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, errors.New("--tiers must not be empty")
	}
	var tiers []fixture.Tier
	seen := map[string]bool{}
	for _, part := range strings.Split(spec, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			return nil, errors.New("--tiers contains an empty entry")
		}
		if seen[name] {
			return nil, fmt.Errorf("--tiers lists %q more than once", name)
		}
		seen[name] = true
		bps, err := parseBitrate(name)
		if err != nil {
			return nil, err
		}
		tiers = append(tiers, fixture.Tier{Name: name, BandwidthBPS: bps})
	}
	return tiers, nil
}

func parseBitrate(s string) (int, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid tier %q: expected e.g. 900k", s)
	}
	mult := 1
	num := s
	switch suffix := s[len(s)-1]; suffix {
	case 'k', 'K':
		mult = 1_000
		num = s[:len(s)-1]
	case 'm', 'M':
		mult = 1_000_000
		num = s[:len(s)-1]
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid tier %q: bad numeric bitrate", s)
	}
	return n * mult, nil
}

// parseDropSpec parses "900k:4" into a tier name and segment index.
func parseDropSpec(spec string) (string, int, error) {
	parts := strings.Split(spec, ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("--drop-segment expects tier:index, got %q", spec)
	}
	idx, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return "", 0, fmt.Errorf("--drop-segment index %q is not numeric", parts[1])
	}
	return strings.TrimSpace(parts[0]), idx, nil
}

// parseSwapSpec parses "1800k:2:3" into a tier name and two indices.
func parseSwapSpec(spec string) (string, int, int, error) {
	parts := strings.Split(spec, ":")
	if len(parts) != 3 {
		return "", 0, 0, fmt.Errorf(
			"--swap-segments expects tier:a:b, got %q", spec)
	}
	a, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return "", 0, 0, fmt.Errorf("swap index %q not numeric", parts[1])
	}
	b, err := strconv.Atoi(strings.TrimSpace(parts[2]))
	if err != nil {
		return "", 0, 0, fmt.Errorf("swap index %q not numeric", parts[2])
	}
	return strings.TrimSpace(parts[0]), a, b, nil
}

// ensureFault returns the fault record for a tier, creating it on first use.
func ensureFault(faults map[string]build.TierFault, tier string) build.TierFault {
	return faults[tier]
}

// validateFaultTiers makes sure every fault references a declared tier.
func validateFaultTiers(tiers []fixture.Tier, faults map[string]build.TierFault) error {
	names := map[string]bool{}
	for _, t := range tiers {
		names[t.Name] = true
	}
	for name := range faults {
		if !names[name] {
			return fmt.Errorf("fault references unknown tier %q", name)
		}
	}
	return nil
}
