// Package verify re-reads an hlsforge build from disk and checks that the
// on-disk artifacts agree with each other and with a freshly rebuilt,
// canonical manifest produced by the same generator used at build time.
package verify

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hlsforge/internal/ingest"
	"hlsforge/internal/manifest"
	"hlsforge/internal/report"
	"hlsforge/internal/segment"
	"hlsforge/internal/timeline"
)

// Input bundles the directories and tolerances for a verification run.
type Input struct {
	Inbox       string
	HLS         string
	FromSeq     int
	ToSeq       int
	PartMs      int64
	SegmentMs   int64
	ToleranceMs int64
}

// Verify performs all checks and returns findings plus per-track summaries.
func Verify(in Input) ([]report.TrackSummary, []report.Finding, error) {
	collected, err := ingest.Collect(in.Inbox, in.FromSeq, in.ToSeq, in.PartMs)
	if err != nil {
		return nil, nil, err
	}
	var findings []report.Finding
	// Surface ingest-time findings (missing parts, conflicts, late arrivals).
	findings = append(findings, collected.Findings...)
	var inputs []manifest.TrackInput
	var tracks []*timeline.Track
	summaries := make([]report.TrackSummary, 0, len(collected.Tracks))

	for _, ct := range collected.Tracks {
		tr, err := timeline.Build(ct)
		if err != nil {
			return nil, nil, err
		}
		tracks = append(tracks, tr)
		layout, err := segment.Layout(tr, in.SegmentMs, in.PartMs)
		if err != nil {
			return nil, nil, err
		}
		ti := manifest.TrackInput{
			Bitrate: ct.Bitrate, Layout: layout,
			InitURI:  filepath.Join(layout.SegmentDir, "init.mp4"),
			HasPreload: true, CanSkipTo: true,
		}
		inputs = append(inputs, ti)

		sum := report.TrackSummary{
			BitrateKbps: ct.Bitrate / 1000,
			Fragments:   len(ct.Present()),
			Segments:    len(layout.Segments),
			Parts:       layout.PartCount(),
			DurationS:   float64(tr.EndMs) / 1000.0,
		}
		findings = append(findings, checkFiles(in.HLS, ct, tr, layout)...)
		sum.Failures = countFor(findings, ct.Bitrate)
		summaries = append(summaries, sum)
	}

	drift, maxDev := timeline.Compare(tracks, in.ToleranceMs)
	findings = append(findings, drift...)
	for i := range summaries {
		summaries[i].DeviationMs = deviationFor(tracks, summaries[i].BitrateKbps*1000, maxDev)
		summaries[i].Failures = countFor(findings, summaries[i].BitrateKbps*1000)
	}

	findings = append(findings, checkManifests(in.HLS, inputs)...)
	// Refresh failure counts after manifest checks.
	for i := range summaries {
		summaries[i].Failures = countFor(findings, summaries[i].BitrateKbps*1000)
	}
	return summaries, findings, nil
}

// checkFiles confirms every referenced part/segment exists and that each
// content file's SHA-256 matches the fragment hash recorded at ingest.
func checkFiles(hls string, ct *ingest.TrackIngest, tr *timeline.Track,
	layout *segment.TrackSegments) []report.Finding {
	var findings []report.Finding
	for _, seg := range layout.Segments {
		for _, p := range seg.Parts {
			full := filepath.Join(hls, p.URI)
			if p.Gap {
				if _, err := os.Stat(full + ".gap"); err != nil {
					findings = append(findings, report.Finding{
						Code: report.CodeMissingFile, Bitrate: ct.Bitrate,
						Seq: p.PartSeq, Detail: "gap marker missing for " + p.URI})
				}
				continue
			}
			raw, err := os.ReadFile(full)
			if err != nil {
				findings = append(findings, report.Finding{
					Code: report.CodeMissingFile, Bitrate: ct.Bitrate,
					Seq: p.PartSeq, Detail: "part file unreadable: " + p.URI})
				continue
			}
			part := tr.Parts[p.PartSeq-tr.FromSeq]
			if part.Fragment == nil || sha256.Sum256(raw) != part.Fragment.SHA256 {
				findings = append(findings, report.Finding{
					Code: report.CodeHashMismatch, Bitrate: ct.Bitrate,
					Seq: p.PartSeq, Detail: "SHA-256 mismatch for " + p.URI})
			}
		}
		if !seg.HasContent {
			if _, err := os.Stat(filepath.Join(hls, seg.URI+".gap")); err != nil {
				findings = append(findings, report.Finding{
					Code: report.CodeMissingFile, Bitrate: ct.Bitrate,
					Seq: seg.Parts[0].PartSeq, Detail: "segment gap marker missing"})
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(hls, seg.URI)); err != nil {
			findings = append(findings, report.Finding{
				Code: report.CodeMissingFile, Bitrate: ct.Bitrate,
				Seq: seg.Parts[0].PartSeq, Detail: "segment file missing: " + seg.URI})
		}
	}
	return findings
}

// checkManifests rebuilds canonical playlists with the same generator and
// diffs them line by line, reporting the offending bitrate and field.
func checkManifests(hls string, inputs []manifest.TrackInput) []report.Finding {
	var findings []report.Finding
	for _, ti := range inputs {
		path := filepath.Join(hls, ti.Layout.MediaPlaylist)
		onDisk, err := os.ReadFile(path)
		if err != nil {
			findings = append(findings, report.Finding{
				Code: report.CodeMissingFile, Bitrate: ti.Bitrate,
				Detail: "media playlist unreadable: " + ti.Layout.MediaPlaylist})
			continue
		}
		canonical := manifest.MediaPlaylist(ti)
		if f := diffPlaylist(ti.Bitrate, string(onDisk), canonical); f != nil {
			findings = append(findings, *f)
		}
	}
	masterPath := filepath.Join(hls, "master.m3u8")
	onDisk, err := os.ReadFile(masterPath)
	if err != nil {
		findings = append(findings, report.Finding{
			Code: report.CodeMissingFile, Detail: "master playlist unreadable"})
		return findings
	}
	if f := diffPlaylist(0, string(onDisk), manifest.MasterPlaylist(inputs)); f != nil {
		findings = append(findings, *f)
	}
	return findings
}

// diffPlaylist returns a finding naming the first divergent line, including
// the HLS tag (field) that differs.
func diffPlaylist(bitrate int, got, want string) *report.Finding {
	gotLines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	wantLines := strings.Split(strings.TrimRight(want, "\n"), "\n")
	n := len(gotLines)
	if len(wantLines) > n {
		n = len(wantLines)
	}
	for i := 0; i < n; i++ {
		var g, w string
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g == w {
			continue
		}
		return &report.Finding{
			Code: report.CodeManifestDiff, Bitrate: bitrate, Seq: i + 1,
			Field: tagOf(w, g),
			Detail: fmt.Sprintf("line %d: got %q want %q", i+1, g, w),
		}
	}
	return nil
}

// tagOf extracts the HLS tag name (e.g. EXT-X-PART) used as the field name.
func tagOf(lines ...string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, "#EXT") {
			if i := strings.IndexByte(l, ':'); i > 0 {
				return l[1:i]
			}
			return strings.TrimPrefix(strings.SplitN(l, ":", 2)[0], "#")
		}
	}
	return "playlist"
}

func countFor(findings []report.Finding, bitrate int) int {
	n := 0
	for _, f := range findings {
		if f.Bitrate == bitrate {
			n++
		}
	}
	return n
}

func deviationFor(tracks []*timeline.Track, bitrate int, fallback int64) int64 {
	var ref *timeline.Track
	for _, t := range tracks {
		if t.HasContent {
			ref = t
			break
		}
	}
	for _, t := range tracks {
		if t.Bitrate == bitrate && ref != nil {
			return timeline.Deviation(ref, t)
		}
	}
	return fallback
}
