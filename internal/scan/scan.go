// Package scan discovers segment files on disk, derives their rate, sequence
// number and duration from the file name, and streams a SHA-256 over each.
package scan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"streamcheck/internal/model"
)

// Extension is the only media extension scanning accepts.
const Extension = ".ts"

const (
	defaultBatchSize = 256
	hashBufferSize   = 64 * 1024
	// maxScanFiles bounds total work across every rate in one root, not just
	// per rate; it leaves generous room for a twelve-rung ladder at the
	// per-rate limit.
	maxScanFiles = model.MaxSegments * 32
)

// ErrDuplicate is returned by Build-fatal checks when a number repeats inside
// one rate.
var ErrDuplicate = errors.New("duplicate segment number")

var (
	slugPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	extPattern  = regexp.MustCompile(`(?i)\.ts$`)
)

// Options controls a scan.
type Options struct {
	Root      string
	Rates     []int // empty scans every rate found on disk
	BatchSize int   // directory entries per read; <=0 uses the default
}

// Result is the outcome of scanning one root.
type Result struct {
	Root   string
	Rates  []model.Rate
	Issues []model.Issue
	Files  int
}

// MissingRates returns requested rates that produced no files, in request
// order, so callers can turn a typo into a clear error.
func MissingRates(requested []int, res *Result) []int {
	var missing []int
	for _, rate := range requested {
		if _, ok := res.Rate(rate); !ok {
			missing = append(missing, rate)
		}
	}
	return missing
}

// Rate returns the rate with the given bitrate, if present.
func (r *Result) Rate(bitrate int) (model.Rate, bool) {
	for _, rate := range r.Rates {
		if rate.Rate == bitrate {
			return rate, true
		}
	}
	return model.Rate{}, false
}

// ResolveRoot returns an absolute, symlink-free root that must exist.
func ResolveRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve root %q: %w", root, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat root %q: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("root %q is not a directory", root)
	}
	return resolved, nil
}

// Scan walks the root (non-recursively), parses every .ts file and returns
// rates ordered by bitrate. Segments are ordered by number; duplicate numbers
// are retained and reported so that callers can reject or report them.
func Scan(ctx context.Context, opts Options) (*Result, error) {
	root, err := ResolveRoot(opts.Root)
	if err != nil {
		return nil, err
	}
	batch := opts.BatchSize
	if batch <= 0 {
		batch = defaultBatchSize
	}
	want := make(map[int]bool, len(opts.Rates))
	for _, r := range opts.Rates {
		want[r] = true
	}

	dir, err := os.Open(root)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	defer dir.Close()

	res := &Result{Root: root}
	byRate := map[int][]model.Segment{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("scan cancelled: %w", err)
		}
		entries, readErr := dir.ReadDir(batch)
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !extPattern.MatchString(name) {
				continue
			}
			path := filepath.Join(root, name)
			if err := withinRoot(root, path); err != nil {
				return nil, err
			}
			seg, err := ParseName(name, path)
			if err != nil {
				return nil, err
			}
			if len(want) > 0 && !want[seg.Rate] {
				continue
			}
			if seg.Number >= model.MaxSegments {
				return nil, fmt.Errorf("%s: segment number %d exceeds limit %d", name, seg.Number, model.MaxSegments)
			}
			info, err := entry.Info()
			if err != nil {
				return nil, fmt.Errorf("stat %s: %w", name, err)
			}
			seg.Size = info.Size()
			if err := hashFile(ctx, path, &seg); err != nil {
				return nil, err
			}
			byRate[seg.Rate] = append(byRate[seg.Rate], seg)
			if len(byRate[seg.Rate]) > model.MaxSegments {
				return nil, fmt.Errorf("rate %d exceeds limit of %d segments", seg.Rate, model.MaxSegments)
			}
			res.Files++
			if res.Files > maxScanFiles {
				return nil, fmt.Errorf("scan exceeded %d files", maxScanFiles)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read root: %w", readErr)
		}
	}

	rates := make([]int, 0, len(byRate))
	for rate := range byRate {
		rates = append(rates, rate)
	}
	sort.Ints(rates)
	for _, rate := range rates {
		segments := byRate[rate]
		sort.SliceStable(segments, func(i, j int) bool { return segments[i].Number < segments[j].Number })
		seen := map[int]bool{}
		for _, seg := range segments {
			if seen[seg.Number] {
				res.Issues = append(res.Issues, model.Issue{
					Kind:   model.IssueDuplicate,
					Rate:   rate,
					Number: seg.Number,
					Message: fmt.Sprintf("number %d appears more than once in rate %d (%s)",
						seg.Number, rate, filepath.Base(seg.Path)),
				})
				continue
			}
			seen[seg.Number] = true
		}
		res.Rates = append(res.Rates, model.Rate{Rate: rate, Segments: segments})
	}
	return res, nil
}

// ParseName extracts rate, number and duration from a file name of the form
// <slug>_<rate>_<number>_<durationMS>.ts. It rejects illegal characters,
// negative numbers and non-positive durations.
func ParseName(name, path string) (model.Segment, error) {
	base := name
	if ext := filepath.Ext(base); extPattern.MatchString(base) {
		base = strings.TrimSuffix(base, ext)
	}
	parts := strings.Split(base, "_")
	if len(parts) != 4 {
		return model.Segment{}, fmt.Errorf("%s: expected <slug>_<rate>_<number>_<duration_ms>.ts", name)
	}
	slug, rateText, numberText, durationText := parts[0], parts[1], parts[2], parts[3]
	if !slugPattern.MatchString(slug) {
		return model.Segment{}, fmt.Errorf("%s: slug %q contains illegal characters", name, slug)
	}
	rate, err := parsePositive(name, "rate", rateText)
	if err != nil {
		return model.Segment{}, err
	}
	if strings.HasPrefix(numberText, "-") {
		return model.Segment{}, fmt.Errorf("%s: negative segment number %q", name, numberText)
	}
	number, err := parseNonNegative(name, "number", numberText)
	if err != nil {
		return model.Segment{}, err
	}
	duration, err := parseDurationMS(name, durationText)
	if err != nil {
		return model.Segment{}, err
	}
	return model.Segment{Rate: rate, Number: number, DurationMS: duration, Path: path}, nil
}

func parsePositive(name, field, text string) (int, error) {
	v, err := parseNonNegative(name, field, text)
	if err != nil {
		return 0, err
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s: %s must be positive", name, field)
	}
	return v, nil
}

func parseDurationMS(name, text string) (int64, error) {
	v, err := parseNonNegative(name, "duration_ms", text)
	if err != nil {
		return 0, err
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s: duration must be positive", name)
	}
	return int64(v), nil
}

func parseNonNegative(name, field, text string) (int, error) {
	if text == "" {
		return 0, fmt.Errorf("%s: empty %s", name, field)
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%s: %s %q is not a non-negative integer", name, field, text)
		}
	}
	v, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("%s: %s %q out of range", name, field, text)
	}
	return v, nil
}

// withinRoot rejects paths that escape the root, following symlinks so that a
// link pointing outside cannot smuggle a file in s.
func withinRoot(root, path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%s: path escapes root %s", path, root)
	}
	return nil
}

// hashFile streams the file through SHA-256 so that size never forces the
// whole object into memory.
func hashFile(ctx context.Context, path string, seg *model.Segment) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, hashBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("hash cancelled: %w", err)
		}
		n, err := f.Read(buf)
		if n > 0 {
			if _, werr := h.Write(buf[:n]); werr != nil {
				return fmt.Errorf("hash %s: %w", path, werr)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
	}
	seg.SHA256 = hex.EncodeToString(h.Sum(nil))
	return nil
}
