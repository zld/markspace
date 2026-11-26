// Package evidence defines machine-readable verification evidence and the
// terminal summary emitted by medcheck.
package evidence

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zld/medcheck/pkg/media"
)

// Version is the evidence schema version.
const Version = "medcheck-evidence/v1"

// Check is one expected invariant verification.
type Check struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Expected any    `json:"expected,omitempty"`
	Actual   any    `json:"actual,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// Segment is the hash evidence for one keyframe-aligned segment.
type Segment struct {
	Ordinal      int    `json:"ordinal"`
	TrackID      string `json:"track_id"`
	Codec        string `json:"codec"`
	Offset       int64  `json:"offset"`
	End          int64  `json:"end"`
	Length       int64  `json:"length"`
	SHA256       string `json:"sha256"`
	RepeatSHA256 string `json:"repeat_sha256"`
	RepeatMatch  bool   `json:"repeat_match"`
}

// CodecEvidence summarizes one generated/parsed elementary stream.
type CodecEvidence struct {
	Codec            string `json:"codec"`
	Keyframes        int    `json:"keyframes"`
	RoundTripRecords int    `json:"roundtrip_records"`
	IndexHits        int    `json:"index_hits"`
	StreamSHA256     string `json:"stream_sha256"`
}

// Report is the complete verifiable result of a demo/run.
type Report struct {
	Version string `json:"version"`
	Seed    int64  `json:"seed"`
	Window  int    `json:"window"`

	Checks []Check `json:"checks"`
	OKFlag bool    `json:"all_checks_passed"`

	Codecs           map[string]CodecEvidence `json:"codecs"`
	Segments         []Segment                `json:"segments"`
	RoundTripOK      int                      `json:"roundtrip_ok"`
	RoundTripTotal   int                      `json:"roundtrip_total"`
	IndexHitOK       int                      `json:"index_hit_ok"`
	IndexHitTotal    int                      `json:"index_hit_total"`
	RepeatMatchOK    int                      `json:"repeat_match_ok"`
	RepeatMatchTotal int                      `json:"repeat_match_total"`

	Recovered     int `json:"recovered"`
	Unrecoverable int `json:"unrecoverable_gaps"`
	Duplicates    int `json:"duplicates"`
	LatePackets   int `json:"late_packets"`
	SequenceWraps int `json:"sequence_wraps"`

	AVOffsetUS  int64 `json:"av_offset_us"`
	DriftUS     int64 `json:"timeline_drift_us"`
	MaxAVDiffUS int64 `json:"max_av_diff_us"`

	Replacement *ReplacementEvidence `json:"replacement,omitempty"`
	Cache       []CacheEvidence      `json:"cache_scenarios,omitempty"`
	Issues      media.Issues         `json:"issues"`
	OutputFiles []string             `json:"output_files"`
}

// ReplacementEvidence proves same-path replacement retained old and filed new.
type ReplacementEvidence struct {
	LogicalPath string `json:"logical_path"`
	OldAssetID  string `json:"old_asset_id"`
	NewAssetID  string `json:"new_asset_id"`
	OldRetained bool   `json:"old_retained"`
}

// CacheEvidence records one invalidation scenario.
type CacheEvidence struct {
	Scenario    string           `json:"scenario"`
	Expected    string           `json:"expected"`
	State       media.CacheState `json:"state"`
	StreamCache string           `json:"stream_cache"`
}

// New creates an empty report.
func New(seed int64, window int) *Report {
	return &Report{Version: Version, Seed: seed, Window: window, Codecs: map[string]CodecEvidence{}}
}

// Check records a named expectation and updates the aggregate status.
func (r *Report) Check(name string, ok bool, expected, actual any, detail string) {
	r.Checks = append(r.Checks, Check{Name: name, OK: ok, Expected: expected, Actual: actual, Detail: detail})
}

// AddIssue appends corruption/network evidence.
func (r *Report) AddIssue(e *media.Error) {
	if e != nil {
		r.Issues = append(r.Issues, e)
	}
}

// OK recalculates and returns aggregate success.
func (r *Report) OK() bool {
	r.OKFlag = true
	for _, c := range r.Checks {
		if !c.OK {
			r.OKFlag = false
			return false
		}
	}
	return true
}

// Failed returns names of failed checks.
func (r *Report) Failed() []string {
	var out []string
	for _, c := range r.Checks {
		if !c.OK {
			out = append(out, fmt.Sprintf("%s(expected=%v actual=%v)", c.Name, c.Expected, c.Actual))
		}
	}
	return out
}

// Write atomically writes evidence.json under outDir and returns its path.
func (r *Report) Write(outDir string) (string, error) {
	r.OK()
	p, err := media.WriteSafeJSON(outDir, "evidence.json", r)
	if err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(p)
	r.OutputFiles = append(r.OutputFiles, abs)
	return p, nil
}

// TerminalLines returns the stable key=value evidence required by acceptance.
func (r *Report) TerminalLines() []string {
	lines := []string{
		fmt.Sprintf("seed=%d window=%d", r.Seed, r.Window),
	}
	codecNames := make([]string, 0, len(r.Codecs))
	for name := range r.Codecs {
		codecNames = append(codecNames, name)
	}
	sort.Strings(codecNames)
	for _, name := range codecNames {
		c := r.Codecs[name]
		lines = append(lines, fmt.Sprintf("codec=%s keyframes=%d roundtrip=%d index_hits=%d stream_sha256=%s",
			c.Codec, c.Keyframes, c.RoundTripRecords, c.IndexHits, short(c.StreamSHA256)))
	}
	lines = append(lines,
		fmt.Sprintf("roundtrip=%d/%d", r.RoundTripOK, r.RoundTripTotal),
		fmt.Sprintf("index_hit=%d/%d", r.IndexHitOK, r.IndexHitTotal),
		fmt.Sprintf("segments=%d", len(r.Segments)),
	)
	for _, s := range r.Segments {
		lines = append(lines, fmt.Sprintf("segment_%02d sha256=%s repeat_match=%t", s.Ordinal, s.SHA256, s.RepeatMatch))
	}
	lines = append(lines,
		fmt.Sprintf("repeat_match=%d/%d", r.RepeatMatchOK, r.RepeatMatchTotal),
		fmt.Sprintf("recovered=%d", r.Recovered),
		fmt.Sprintf("unrecoverable_gap=%d", r.Unrecoverable),
		fmt.Sprintf("duplicate=%d late=%d rtp_wrap=%d", r.Duplicates, r.LatePackets, r.SequenceWraps),
		fmt.Sprintf("av_offset_us=%d timeline_drift_max_us=%d max_av_diff_us=%d", r.AVOffsetUS, r.DriftUS, r.MaxAVDiffUS),
	)
	if r.Replacement != nil {
		lines = append(lines, fmt.Sprintf("replacement old_retained=%t old=%s new=%s", r.Replacement.OldRetained, short(r.Replacement.OldAssetID), short(r.Replacement.NewAssetID)))
	}
	for _, c := range r.Cache {
		lines = append(lines, fmt.Sprintf("cache %s stream_parse=%s offset_mapping=%s timeline_index=%s", c.Scenario, c.State.StreamParse, c.State.OffsetMapping, c.State.TimelineIndex))
	}
	var corrupt int
	for _, is := range []media.Issues{r.Issues} {
		corrupt += len(is)
	}
	lines = append(lines, fmt.Sprintf("error_records=%d", len(r.Issues)))
	for _, e := range r.Issues {
		lines = append(lines, "issue "+e.Error())
	}
	if failed := r.Failed(); len(failed) > 0 {
		lines = append(lines, "FAILED: "+strings.Join(failed, "; "))
	} else {
		lines = append(lines, "medcheck=PASS")
	}
	return lines
}

func short(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}
