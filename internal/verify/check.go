// Package verify independently checks a produced clip against its
// manifest: it re-reads the input set, recomputes boundaries, bitrate and
// quality metrics, and reports hard-constraint failures.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"replay/internal/cmaf"
	"replay/internal/events"
	"replay/internal/manifest"
	"replay/internal/model"
)

// Constraints are the hard delivery limits.
type Constraints struct {
	MaxBitrateDev  float64       // e.g. 0.10 for 10%
	MinPSNR        float64       // dB
	MaxBoundaryErr time.Duration // e.g. 100ms
	EventTolerance time.Duration // tolerance used when recomputing windows
}

// DefaultConstraints returns the acceptance limits.
func DefaultConstraints() Constraints {
	return Constraints{
		MaxBitrateDev:  0.10,
		MinPSNR:        30.0,
		MaxBoundaryErr: 100 * time.Millisecond,
		EventTolerance: 500 * time.Millisecond,
	}
}

// Check is one verified constraint.
type Check struct {
	Name   string
	OK     bool
	Detail string
}

// Outcome aggregates all checks. OK is false when any hard check failed.
type Outcome struct {
	Checks []Check
	OK     bool
}

func (o *Outcome) add(name string, ok bool, format string, args ...any) {
	o.Checks = append(o.Checks, Check{Name: name, OK: ok, Detail: fmt.Sprintf(format, args...)})
	if !ok {
		o.OK = false
	}
}

// BitrateDev returns |actual-target|/target.
func BitrateDev(actual, target int64) float64 {
	if target <= 0 {
		return 0
	}
	d := float64(actual - target)
	if d < 0 {
		d = -d
	}
	return d / float64(target)
}

var psnrRe = regexp.MustCompile(`average:([0-9.]+|inf)`)

// ParsePSNRAverage extracts the average PSNR from ffmpeg's psnr output.
func ParsePSNRAverage(log string) (float64, error) {
	m := psnrRe.FindStringSubmatch(log)
	if m == nil {
		return 0, fmt.Errorf("verify: no PSNR average in output")
	}
	if m[1] == "inf" {
		return 99.0, nil // identical streams: treat as perfect
	}
	return strconv.ParseFloat(m[1], 64)
}

// sha256File hashes a file's contents.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Verify checks the clip described by the manifest at mpath. setDir holds
// the input init segment and fragments; eventsPath is the event record
// file used to recompute the window independently.
func Verify(mpath, setDir, eventsPath string, c Constraints) (Outcome, error) {
	o := Outcome{OK: true}
	m, err := manifest.Load(mpath)
	if err != nil {
		return o, err
	}

	// 1. manifest self hash
	h, err := manifest.ComputeHash(m)
	if err != nil {
		return o, err
	}
	o.add("manifest_hash", h == m.Hash, "stored=%s recomputed=%s", m.Hash, h)

	// 2. output identity
	outPath := m.Output.Path
	if !filepath.IsAbs(outPath) {
		outPath = filepath.Join(filepath.Dir(mpath), outPath)
	}
	st, err := os.Stat(outPath)
	if err != nil {
		o.add("output_exists", false, "%s", err)
		return o, nil
	}
	sum, err := sha256File(outPath)
	if err != nil {
		return o, err
	}
	o.add("output_hash", sum == m.Output.SHA256 && st.Size() == m.Output.SizeBytes,
		"sha256=%s size=%d", sum, st.Size())

	// 3. boundaries from the actual media
	dur, start, err := probeTiming(outPath)
	if err != nil {
		return o, err
	}
	wantDur := time.Duration(m.Window.EndMS-m.Window.StartMS) * time.Millisecond
	durErr := absDur(dur - wantDur)
	startErr := absDur(start)
	bErr := durErr
	if startErr > bErr {
		bErr = startErr
	}
	o.add("boundary", bErr <= c.MaxBoundaryErr,
		"max_err=%v (duration_err=%v start_err=%v) limit=%v", bErr, durErr, startErr, c.MaxBoundaryErr)

	// 4. bitrate deviation
	actual := int64(0)
	if dur > 0 {
		actual = st.Size() * 8 * int64(time.Second) / int64(dur)
	}
	dev := BitrateDev(actual, m.Params.TargetBitrate)
	o.add("bitrate", dev <= c.MaxBitrateDev,
		"target=%d actual=%d dev=%.1f%% limit=%.0f%%",
		m.Params.TargetBitrate, actual, dev*100, c.MaxBitrateDev*100)

	// 5. window recomputed independently from events + input timeline
	rep := &model.Report{}
	init, frags, err := loadSet(setDir, rep)
	if err != nil {
		return o, err
	}
	tl := cmaf.BuildTimeline(frags, init, rep)
	evs, err := events.Load(eventsPath)
	if err != nil {
		return o, err
	}
	ev := events.Find(evs, m.Event)
	if ev == nil {
		return o, fmt.Errorf("verify: event %s not found in %s", m.Event, eventsPath)
	}
	win, err := events.WindowFor(*ev,
		time.Duration(m.Params.WindowBeforeMS)*time.Millisecond,
		time.Duration(m.Params.WindowAfterMS)*time.Millisecond,
		tl, c.EventTolerance, &model.Report{})
	if err != nil {
		return o, fmt.Errorf("verify: window recompute: %w", err)
	}
	o.add("window", win.Start.Milliseconds() == m.Window.StartMS && win.End.Milliseconds() == m.Window.EndMS,
		"manifest=[%d,%d] recomputed=[%d,%d]",
		m.Window.StartMS, m.Window.EndMS, win.Start.Milliseconds(), win.End.Milliseconds())

	// 6. error counts recomputed from the input set
	o.add("error_counts",
		tl.SeqGaps == m.Timeline.SeqGaps &&
			tl.Duplicates == m.Timeline.Duplicates &&
			tl.Regressions == m.Timeline.TfdtRegressions &&
			len(tl.Missing) == len(m.Timeline.MissingMS),
		"gaps=%d/%d dups=%d/%d regressions=%d/%d missing=%d/%d (manifest/actual)",
		m.Timeline.SeqGaps, tl.SeqGaps, m.Timeline.Duplicates, tl.Duplicates,
		m.Timeline.TfdtRegressions, tl.Regressions, len(m.Timeline.MissingMS), len(tl.Missing))

	// 7. quality: PSNR against an independently rendered reference
	psnr, err := psnrAgainstReference(m, setDir, outPath)
	if err != nil {
		return o, err
	}
	o.add("psnr", psnr >= c.MinPSNR, "average=%.2f dB limit=%.1f dB", psnr, c.MinPSNR)

	return o, nil
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// loadSet parses init.mp4 and all fragments in setDir, in file order.
func loadSet(setDir string, rep *model.Report) (*cmaf.Init, []*cmaf.Fragment, error) {
	p := cmaf.NewParser(rep)
	init, err := p.ParseInit(filepath.Join(setDir, "init.mp4"))
	if err != nil {
		return nil, nil, fmt.Errorf("verify: parse init: %w", err)
	}
	paths, err := fragmentPaths(setDir)
	if err != nil {
		return nil, nil, err
	}
	frags := make([]*cmaf.Fragment, 0, len(paths))
	for _, fp := range paths {
		f, err := p.ParseFragment(fp, init)
		if err != nil {
			return nil, nil, fmt.Errorf("verify: parse %s: %w", fp, err)
		}
		frags = append(frags, f)
	}
	return init, frags, nil
}
