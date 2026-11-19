// Package verify checks that declared manifests agree one-for-one with the
// files on disk, that segment numbering is sane, and that the rate variants
// share a compatible timeline.
package verify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"streamcheck/internal/manifest"
	"streamcheck/internal/model"
	"streamcheck/internal/scan"
)

// DefaultDeviationMS is the cumulative boundary deviation tolerated between
// rates when no explicit threshold is supplied.
const DefaultDeviationMS int64 = 500

// Options configures a verification run.
type Options struct {
	MissingTolerance     int
	DeviationThresholdMS int64
	Rates                []int // empty verifies every rate found on disk
}

// RateCheck is the per-rate slice of the verification result.
type RateCheck struct {
	Rate                int   `json:"rate"`
	SegmentCount        int   `json:"segment_count"`
	DurationMS          int64 `json:"duration_ms"`
	DurationDeviationMS int64 `json:"duration_deviation_ms"`
	MissingCount        int   `json:"missing_count"`
	OutOfOrderCount     int   `json:"out_of_order_count"`
	DuplicateCount      int   `json:"duplicate_count"`
	ChecksumFailures    int   `json:"checksum_failures"`
	HasManifest         bool  `json:"has_manifest"`
}

// Result is the complete outcome of verification.
type Result struct {
	Root             string
	Rates            []RateCheck
	Issues           []model.Issue
	MissingCount     int
	OutOfOrderCount  int
	DuplicateCount   int
	ChecksumFailures int
	MaxDeviationMS   int64
	MissingTolerance int
}

// Failed reports whether findings exceed the configured missing tolerance or
// contain any non-missing problem.
func (r Result) Failed() bool {
	if r.MissingCount > r.MissingTolerance {
		return true
	}
	for _, issue := range r.Issues {
		if issue.Kind != model.IssueMissing {
			return true
		}
	}
	return false
}

var manifestName = regexp.MustCompile(`^(\d+)\.m3u8$`)

// LoadManifests parses every <rate>.m3u8 in root. It is the only manifest
// reader, so verification and generation share one normalization path.
// Read failures are returned as errors; malformed playlists become issues so
// that one broken rate does not hide the rest.
func LoadManifests(ctx context.Context, root string, rates []int) (map[int]model.Manifest, []model.Issue, error) {
	want := make(map[int]bool, len(rates))
	for _, rate := range rates {
		want[rate] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, fmt.Errorf("read root: %w", err)
	}
	out := make(map[int]model.Manifest)
	var issues []model.Issue
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if entry.IsDir() {
			continue
		}
		match := manifestName.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		rate, err := strconv.Atoi(match[1])
		if err != nil || rate <= 0 {
			continue
		}
		if len(want) > 0 && !want[rate] {
			continue
		}
		path := filepath.Join(root, entry.Name())
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, fmt.Errorf("open manifest %s: %w", entry.Name(), err)
		}
		parsed, perr := manifest.Parse(f, rate)
		f.Close()
		if perr != nil {
			issues = append(issues, model.Issue{
				Kind:    model.IssueUnparseable,
				Rate:    rate,
				Message: fmt.Sprintf("manifest %s: %v", entry.Name(), perr),
			})
			continue
		}
		if len(parsed.Segments) > 0 {
			first := parsed.Segments[0].URI
			if named, nerr := scan.ParseName(first, first); nerr == nil && named.Number != parsed.MediaSequence {
				issues = append(issues, model.Issue{
					Kind:   model.IssueManifest,
					Rate:   rate,
					Number: named.Number,
					Message: fmt.Sprintf("media sequence %d does not match first number %d",
						parsed.MediaSequence, named.Number),
				})
			}
		}
		out[rate] = parsed
	}
	return out, issues, nil
}

// Run verifies a scanned root. scanned may be nil, in which case the root is
// scanned here; the caller normally passes a scan so it can report progress.
func Run(ctx context.Context, root string, scanned *scan.Result, opts Options) (Result, error) {
	manifests, loadIssues, err := LoadManifests(ctx, root, opts.Rates)
	if err != nil {
		return Result{}, err
	}
	if scanned == nil {
		scanned, err = scan.Scan(ctx, scan.Options{Root: root})
		if err != nil {
			return Result{}, err
		}
	}

	res := Result{
		Root:             root,
		Issues:           append(append([]model.Issue(nil), scanned.Issues...), loadIssues...),
		MissingTolerance: opts.MissingTolerance,
	}
	checks := make(map[int]*RateCheck)
	diskRates := make(map[int]model.Rate, len(scanned.Rates))
	for _, rate := range scanned.Rates {
		diskRates[rate.Rate] = rate
		checks[rate.Rate] = &RateCheck{Rate: rate.Rate}
	}
	rateIDs := make([]int, 0, len(checks))
	for rate := range checks {
		rateIDs = append(rateIDs, rate)
	}
	for rate := range manifests {
		if _, ok := checks[rate]; !ok {
			checks[rate] = &RateCheck{Rate: rate}
			rateIDs = append(rateIDs, rate)
		}
	}
	sort.Ints(rateIDs)
	if len(rateIDs) == 0 {
		return Result{}, fmt.Errorf("no segment files or manifests found under %s", root)
	}

	for _, rateID := range rateIDs {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		check := checks[rateID]
		disk := diskRates[rateID]
		parsed, hasManifest := manifests[rateID]
		check.HasManifest = hasManifest
		check.SegmentCount = len(disk.Segments)
		check.DurationMS = disk.DurationMS()
		if hasManifest && len(disk.Segments) == 0 {
			// Nothing on disk: report what the playlist claims so the
			// summary still shows the size of the gap.
			check.DurationMS = parsed.DurationMS()
		}
		if !hasManifest && len(disk.Segments) > 0 {
			// A rate with files but no playlist cannot be checked for
			// correspondence; say so instead of silently passing it.
			parsed = model.Manifest{Rate: rateID}
		}
		rateIssues := verifyRate(disk, parsed, hasManifest)
		res.Issues = append(res.Issues, rateIssues...)
		check.MissingCount, check.OutOfOrderCount, check.DuplicateCount, check.ChecksumFailures = countKinds(rateIssues)
	}

	crossRate(scanned, opts, checks, &res)
	trackChecksums(scanned, checks, &res)

	// Recount per rate from the final issue list so that findings raised
	// before a rate was individually checked (duplicate numbers noticed during
	// scanning, for instance) appear in that rate's row too.
	for _, check := range checks {
		check.MissingCount, check.OutOfOrderCount, check.DuplicateCount, check.ChecksumFailures = 0, 0, 0, 0
	}
	for _, issue := range res.Issues {
		check, ok := checks[issue.Rate]
		if !ok {
			continue
		}
		switch issue.Kind {
		case model.IssueMissing:
			check.MissingCount++
		case model.IssueOutOfOrder:
			check.OutOfOrderCount++
		case model.IssueDuplicate:
			check.DuplicateCount++
		case model.IssueChecksum:
			check.ChecksumFailures++
		}
	}

	for _, rateID := range rateIDs {
		res.Rates = append(res.Rates, *checks[rateID])
	}
	countResult(&res)
	return res, nil
}

// verifyRate compares one rate's manifest against its files. Missing numbers
// are derived once from the union of declared and on-disk numbers, so a hole
// is reported exactly once no matter which side noticed it.
func verifyRate(disk model.Rate, parsed model.Manifest, hasManifest bool) []model.Issue {
	var issues []model.Issue
	diskByBase := make(map[string]model.Segment, len(disk.Segments))
	// macOS volumes are often case-insensitive, so a URI differing only in
	// case still opens. Report that as a naming problem rather than a missing
	// file, because the same playlist breaks on a case-sensitive origin.
	diskByFold := make(map[string]string, len(disk.Segments))
	for _, seg := range disk.Segments {
		base := filepath.Base(seg.Path)
		diskByBase[base] = seg
		diskByFold[strings.ToLower(base)] = base
	}

	numbers := make(map[int]bool)
	for _, seg := range disk.Segments {
		numbers[seg.Number] = true
	}
	missing := make(map[int]bool)

	if !hasManifest && len(disk.Segments) > 0 {
		issues = append(issues, model.Issue{
			Kind:    model.IssueManifest,
			Rate:    disk.Rate,
			Message: fmt.Sprintf("no %d.m3u8 manifest found; %d files cannot be cross-checked", disk.Rate, len(disk.Segments)),
		})
	}

	if hasManifest {
		seenURI := make(map[string]bool, len(parsed.Segments))
		seenNumber := make(map[int]bool, len(parsed.Segments))
		previous := -1
		for _, entry := range parsed.Segments {
			if seenURI[entry.URI] {
				issues = append(issues, model.Issue{
					Kind:    model.IssueDuplicate,
					Rate:    parsed.Rate,
					Message: fmt.Sprintf("manifest declares %s more than once", entry.URI),
				})
				continue
			}
			seenURI[entry.URI] = true

			number := -1
			if named, err := scan.ParseName(entry.URI, entry.URI); err == nil {
				if named.Rate != parsed.Rate {
					issues = append(issues, model.Issue{
						Kind:   model.IssueManifest,
						Rate:   parsed.Rate,
						Number: named.Number,
						Message: fmt.Sprintf("entry %s belongs to rate %d, not %d",
							entry.URI, named.Rate, parsed.Rate),
					})
					continue
				}
				number = named.Number
				numbers[number] = true
				if seenNumber[number] {
					issues = append(issues, model.Issue{
						Kind:    model.IssueDuplicate,
						Rate:    parsed.Rate,
						Number:  number,
						Message: fmt.Sprintf("number %d is declared twice", number),
					})
				}
				seenNumber[number] = true
				if previous >= 0 && number <= previous {
					issues = append(issues, model.Issue{
						Kind:   model.IssueOutOfOrder,
						Rate:   parsed.Rate,
						Number: number,
						Message: fmt.Sprintf("number %d appears after %d in the manifest",
							number, previous),
					})
				}
				previous = number
			}

			seg, onDisk := diskByBase[entry.URI]
			if !onDisk {
				if actual, folded := diskByFold[strings.ToLower(entry.URI)]; folded && actual != entry.URI {
					issues = append(issues, model.Issue{
						Kind:   model.IssueManifest,
						Rate:   parsed.Rate,
						Number: number,
						Message: fmt.Sprintf("manifest names %s but the file is %s; case differs",
							entry.URI, actual),
					})
					delete(diskByBase, actual)
					seenURI[actual] = true
					continue
				}
			}
			if !onDisk {
				missing[number] = true
				issues = append(issues, model.Issue{
					Kind:    model.IssueMissing,
					Rate:    parsed.Rate,
					Number:  number,
					Message: fmt.Sprintf("manifest declares %s but no such file exists", entry.URI),
				})
				continue
			}
			if entry.DurationMS != seg.DurationMS {
				issues = append(issues, model.Issue{
					Kind:   model.IssueDuration,
					Rate:   parsed.Rate,
					Number: seg.Number,
					Message: fmt.Sprintf("manifest says %dms but the file name records %dms",
						entry.DurationMS, seg.DurationMS),
				})
			}
		}
		for base, seg := range diskByBase {
			if !seenURI[base] {
				issues = append(issues, model.Issue{
					Kind:    model.IssueManifest,
					Rate:    parsed.Rate,
					Number:  seg.Number,
					Message: fmt.Sprintf("file %s is not declared by the manifest", base),
				})
			}
		}
	}

	issues = append(issues, gapIssues(disk.Rate, numbers, missing)...)
	return issues
}

// gapIssues reports numbers absent between the lowest and highest observed
// number, skipping holes already reported as declared-but-missing.
func gapIssues(rate int, numbers, reported map[int]bool) []model.Issue {
	if len(numbers) == 0 {
		return nil
	}
	lo, hi := -1, -1
	for number := range numbers {
		if lo < 0 || number < lo {
			lo = number
		}
		if number > hi {
			hi = number
		}
	}
	var issues []model.Issue
	for number := lo + 1; number < hi; number++ {
		if numbers[number] || reported[number] {
			continue
		}
		issues = append(issues, model.Issue{
			Kind:    model.IssueMissing,
			Rate:    rate,
			Number:  number,
			Message: fmt.Sprintf("number %d is absent between %d and %d", number, lo, hi),
		})
	}
	return issues
}

func countKinds(issues []model.Issue) (missing, outOfOrder, duplicate, checksum int) {
	for _, issue := range issues {
		switch issue.Kind {
		case model.IssueMissing:
			missing++
		case model.IssueOutOfOrder:
			outOfOrder++
		case model.IssueDuplicate:
			duplicate++
		case model.IssueChecksum:
			checksum++
		}
	}
	return
}

// crossRate compares counts, cumulative duration and boundary timing against
// the lowest-bitrate rate as the timeline reference.
func crossRate(scanned *scan.Result, opts Options, checks map[int]*RateCheck, res *Result) {
	if len(scanned.Rates) < 2 {
		return
	}
	threshold := opts.DeviationThresholdMS
	if threshold <= 0 {
		threshold = DefaultDeviationMS
	}
	ordered := make([]model.Rate, len(scanned.Rates))
	copy(ordered, scanned.Rates)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Rate < ordered[j].Rate })

	timelines := make(map[int][]int64, len(ordered))
	for _, rate := range ordered {
		cumulative := make([]int64, 0, len(rate.Segments))
		var total int64
		seen := make(map[int]bool, len(rate.Segments))
		for _, seg := range rate.Segments { // already sorted by number
			if seen[seg.Number] {
				continue
			}
			seen[seg.Number] = true
			total += seg.DurationMS
			cumulative = append(cumulative, total)
		}
		timelines[rate.Rate] = cumulative
	}

	reference := ordered[0].Rate
	ref := timelines[reference]
	for _, rate := range ordered[1:] {
		other := timelines[rate.Rate]
		check := checks[rate.Rate]
		count := len(ref)
		if len(other) < count {
			count = len(other)
		}
		var deviation int64
		for i := 0; i < count; i++ {
			if diff := abs64(other[i] - ref[i]); diff > deviation {
				deviation = diff
			}
		}
		if len(other) > 0 && len(ref) > 0 {
			if diff := abs64(other[len(other)-1] - ref[len(ref)-1]); diff > deviation {
				deviation = diff
			}
		}
		check.DurationDeviationMS = deviation
		if deviation > res.MaxDeviationMS {
			res.MaxDeviationMS = deviation
		}
		if len(other) != len(ref) {
			res.Issues = append(res.Issues, model.Issue{
				Kind: model.IssueCrossRate,
				Rate: rate.Rate,
				Message: fmt.Sprintf("segment count %d differs from rate %d count %d",
					len(other), reference, len(ref)),
			})
		}
		if deviation > threshold {
			res.Issues = append(res.Issues, model.Issue{
				Kind: model.IssueCrossRate,
				Rate: rate.Rate,
				Message: fmt.Sprintf("cumulative boundary deviation %dms exceeds threshold %dms against rate %d",
					deviation, threshold, reference),
			})
		}
	}
}

// trackChecksums reports content repeated within a rate. Identical bytes
// across different rates are allowed by design.
func trackChecksums(scanned *scan.Result, checks map[int]*RateCheck, res *Result) {
	for _, rate := range scanned.Rates {
		seen := make(map[string]int, len(rate.Segments))
		for _, seg := range rate.Segments {
			if seg.SHA256 == "" {
				continue
			}
			first, exists := seen[seg.SHA256]
			if exists && first != seg.Number {
				res.Issues = append(res.Issues, model.Issue{
					Kind:   model.IssueChecksum,
					Rate:   rate.Rate,
					Number: seg.Number,
					Message: fmt.Sprintf("content duplicates number %d in the same rate",
						first),
				})
				if check := checks[rate.Rate]; check != nil {
					check.ChecksumFailures++
				}
				continue
			}
			if !exists {
				seen[seg.SHA256] = seg.Number
			}
		}
	}
}

func countResult(res *Result) {
	res.MissingCount, res.OutOfOrderCount, res.DuplicateCount, res.ChecksumFailures = 0, 0, 0, 0
	for _, issue := range res.Issues {
		switch issue.Kind {
		case model.IssueMissing:
			res.MissingCount++
		case model.IssueOutOfOrder:
			res.OutOfOrderCount++
		case model.IssueDuplicate:
			res.DuplicateCount++
		case model.IssueChecksum:
			res.ChecksumFailures++
		}
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
