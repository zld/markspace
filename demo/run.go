// Package demo runs the end-to-end acceptance scenario: generate fixtures, push
// them through the reusable kernel, and collect every number the report prints.
//
// Nothing here depends on the source tree layout; all artefacts go into the
// caller-provided directory.
package demo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediarc/model"
	rtpkernel "mediarc/rtp"
)

// Options are the acceptance-run knobs.
type Options struct {
	// Dir is where fixtures and artefacts are written.
	Dir string
	// Seed shifts the RTP damage cadence. The same seed always regenerates
	// byte-identical fixtures.
	Seed int64
	// Window is the RTP jitter window depth.
	Window int
	// MaxBuffer is the per-stage pipeline capacity.
	MaxBuffer int
}

func (o Options) validate() (Options, error) {
	if o.Dir == "" {
		return Options{}, model.New(model.CodeInvalidArgument, "demo", "output directory is required")
	}
	if o.Window <= 0 {
		return Options{}, model.New(model.CodeInvalidArgument, "demo",
			fmt.Sprintf("window depth must be positive, got %d", o.Window))
	}
	if o.MaxBuffer <= 0 {
		return Options{}, model.New(model.CodeInvalidArgument, "demo",
			fmt.Sprintf("max buffer must be positive, got %d", o.MaxBuffer))
	}
	return o, nil
}

// Report is the full acceptance result.
type Report struct {
	Scenarios []Scenario
	Failures  []string
	Fixtures  []string
	Manifest  string
	Metrics   Metrics
}

// OK reports whether every assertion passed.
func (r Report) OK() bool { return len(r.Failures) == 0 }

// Scenario is the outcome for one fixture family.
type Scenario struct {
	Name   string
	Detail string
	Checks []Assertion
}

// Assertion is one acceptance check.
type Assertion struct {
	What string
	Got  string
	Want string
	OK   bool
}

// Metrics carries the aggregate numbers the report prints.
type Metrics struct {
	FrameHash    uint64
	SegmentHash  uint64
	PixelMean    float64
	PeakDBFS     float64
	PacketsLost  uint64
	Recovered    int
	FirstBadOff  int64
	MaxAlignErr  int64
	CueChecksum  uint64
	FrameCount   int
	SegmentCount int
}

// Run executes the whole acceptance scenario.
func Run(ctx context.Context, opts Options) (Report, error) {
	opts, err := opts.validate()
	if err != nil {
		return Report{}, err
	}
	fixtureDir := filepath.Join(opts.Dir, "fixtures")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		return Report{}, model.Fatal(model.CodeInvalidArgument, "demo", "cannot create fixture directory").WithCause(err)
	}
	r := &runner{opts: opts, fixtureDir: fixtureDir, report: Report{}}
	if err := r.run(ctx); err != nil {
		return r.report, err
	}
	r.report.Metrics = r.metrics
	if err := r.writeManifest(); err != nil {
		return r.report, err
	}
	return r.report, nil
}

type runner struct {
	opts       Options
	fixtureDir string
	report     Report
	metrics    Metrics
	segments   []rtpkernel.Segment
}

func (r *runner) fail(what, got, want string) {
	r.report.Failures = append(r.report.Failures, fmt.Sprintf("%s: got %s, want %s", what, got, want))
}

func (r *runner) addFixture(name string) {
	r.report.Fixtures = append(r.report.Fixtures, name)
}

func (r *runner) record(scenario Scenario) {
	for _, assertion := range scenario.Checks {
		if !assertion.OK {
			r.fail(assertion.What, assertion.Got, assertion.Want)
		}
	}
	r.report.Scenarios = append(r.report.Scenarios, scenario)
}

func (r *runner) path(name string) string { return filepath.Join(r.fixtureDir, name) }

// scenario is one acceptance family: it generates its fixtures, exercises the
// kernel and records its checks.
type scenario struct {
	name  string
	order int
	run   func(*runner, context.Context) error
}

// registry is filled by each scenario file's init, so a scenario can be added
// or reviewed on its own without touching the dispatcher. The explicit order
// keeps rendering (which consumes the RTP segments) after the RTP scenario
// regardless of file initialisation order.
var registry []scenario

func register(name string, order int, run func(*runner, context.Context) error) {
	registry = append(registry, scenario{name: name, order: order, run: run})
}

func (r *runner) run(ctx context.Context) error {
	ordered := make([]scenario, len(registry))
	copy(ordered, registry)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].order < ordered[j].order })
	for _, s := range ordered {
		if err := s.run(r, ctx); err != nil {
			return err
		}
	}
	return nil
}

func assert(what, got, want string, ok bool) Assertion {
	return Assertion{What: what, Got: got, Want: want, OK: ok}
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
