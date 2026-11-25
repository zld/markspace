// Package pipeline composes the subsystems: it parses an input, validates
// tracks and fragments, and drives manifest migration. The CLI only calls into
// this package, so every acceptance command exercises the reusable library.
package pipeline

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"

	"example.com/assetcheck/internal/fragments"
	"example.com/assetcheck/internal/manifest"
	"example.com/assetcheck/internal/matroska"
	"example.com/assetcheck/internal/media"
	"example.com/assetcheck/internal/migrate"
	"example.com/assetcheck/internal/mp4index"
	"example.com/assetcheck/internal/rtpcheck"
	"example.com/assetcheck/internal/timeline"
	"example.com/assetcheck/internal/tracks"
)

// Container sniffing reads only the first bytes of the input.
var ebmlMagic = []byte{0x1A, 0x45, 0xDF, 0xA3}

// ParseInput detects the container from its magic bytes and parses it into the
// shared media model. The same file bytes feed every later stage.
func ParseInput(path string) (*media.Asset, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, 0, -1,
			"open %s", path)
	}
	head := make([]byte, 4)
	n, err := io.ReadFull(f, head)
	f.Close()
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, timeline.New(timeline.StageParse, timeline.CatTruncated, 0, 0,
				"input %s is only %d bytes; no container header", path, n)
		}
		return nil, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, 0, -1, "read header")
	}
	if bytes.Equal(head, ebmlMagic) {
		return matroska.Parse(path)
	}
	if isMP4Head(head) {
		return mp4index.Parse(path)
	}
	return nil, timeline.New(timeline.StageParse, timeline.CatUnsupported, 0, 0,
		"input %s is neither Matroska (EBML 0x1A45DFA3) nor MP4 (%q)", path, string(head))
}

func isMP4Head(head []byte) bool {
	// A top-level MP4 box carries its size in the first four bytes followed by
	// a known type; ftyp, moov, mdat, free and sidx are all accepted.
	known := map[string]bool{"ftyp": true, "moov": true, "mdat": true, "free": true, "sidx": true}
	return known[string(head)]
}

// VerifyOptions configure a verification run.
type VerifyOptions struct {
	// Profile is the manifest-derived envelope. The zero profile only checks
	// internal consistency.
	Profile manifest.Profile
	// RTPDump optionally validates a companion capture.
	RTPDump string
}

// VerifyReport is the quantitative, terminal-printable result.
type VerifyReport struct {
	Asset         *media.Asset
	ElementCount  int
	TrackCount    int
	FragmentCount int
	MaxDeviation  timeline.Nanosecond
	DeviationAt   int64
	Tracks        []tracks.Track
	Issues        []tracks.Issue
	Fragments     fragments.Report
	RTP           *rtpcheck.Report
	SHA256        string
}

// OK reports whether every check passed.
func (r *VerifyReport) OK() bool {
	return len(r.Issues) == 0 && r.Fragments.OK() && (r.RTP == nil || r.RTP.OK())
}

// Err returns the earliest failure as a structured error, or nil. Parse-stage
// failures are returned by Verify directly; this covers validation failures.
func (r *VerifyReport) Err() error {
	candidates := make([]*timeline.Error, 0, len(r.Issues)+len(r.Fragments.Problems))
	for _, is := range r.Issues {
		candidates = append(candidates, is.ToError(timeline.StageValidate))
	}
	for _, p := range r.Fragments.Problems {
		var te *timeline.Error
		if err := pErr(p, &te); err {
			candidates = append(candidates, te)
		}
	}
	if r.RTP != nil {
		for _, p := range r.RTP.Problems {
			candidates = append(candidates, &timeline.Error{
				Stage: timeline.StageValidate, Category: p.Category, Offset: -1,
				Tracks: []uint64{p.Track}, Reason: p.Reason,
			})
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Offset < candidates[j].Offset
	})
	return candidates[0]
}

func pErr(p fragments.Problem, target **timeline.Error) bool {
	*target = &timeline.Error{
		Stage: timeline.StageValidate, Category: p.Category, ElementID: p.ElementID,
		Offset: p.Offset, Tracks: p.Tracks, Reason: p.Reason,
	}
	return true
}

// Verify parses and validates one input.
func Verify(path string, opts VerifyOptions) (*VerifyReport, error) {
	asset, err := ParseInput(path)
	if err != nil {
		return nil, err
	}
	profile := tracks.Profile{
		AudioRates:    opts.Profile.AudioRates,
		Layouts:       opts.Profile.Layouts,
		VideoColour:   opts.Profile.VideoColour,
		MaxChannels:   opts.Profile.MaxChannels,
		RequireColour: opts.Profile.RequireColour,
	}
	issues := tracks.Validate(asset.Tracks, profile)
	frag := fragments.Validate(asset)
	rep := &VerifyReport{
		Asset: asset, ElementCount: asset.ElementCount(), TrackCount: len(asset.Tracks),
		FragmentCount: len(asset.Fragments), MaxDeviation: frag.MaxDeviation,
		DeviationAt: frag.DeviationAt, Tracks: asset.Tracks, Issues: issues,
		Fragments: frag, SHA256: asset.SHA256,
	}
	if opts.RTPDump != "" {
		recs, _, err := rtpcheck.ParseDump(opts.RTPDump)
		if err != nil {
			return nil, err
		}
		rtp := rtpcheck.Validate(recs, asset)
		rep.RTP = &rtp
	}
	if err := rep.Err(); err != nil {
		return rep, err
	}
	return rep, nil
}

// MigrateOptions configure a migration run.
type MigrateOptions struct {
	Input        string
	ManifestPath string
	Out          string
	StopAfter    string
	Resume       bool
}

// MigrateOutcome combines verification and migration results.
type MigrateOutcome struct {
	Verify *VerifyReport
	Result *migrate.Result
}

// Migrate verifies the input against the manifest, then runs the staged
// migration. Validation failures stop before any checkpoint is written.
func Migrate(opts MigrateOptions) (*MigrateOutcome, error) {
	m, err := manifest.Load(opts.ManifestPath)
	if err != nil {
		return nil, err
	}
	if err := m.ValidateCrossFields(); err != nil {
		return nil, err
	}
	asset, err := ParseInput(opts.Input)
	if err != nil {
		return nil, err
	}
	profile := tracks.Profile{
		AudioRates:    m.AudioRates,
		Layouts:       m.Layouts,
		VideoColour:   m.VideoColour,
		MaxChannels:   m.MaxChannels,
		RequireColour: m.RequireColour,
	}
	issues := tracks.Validate(asset.Tracks, profile)
	frag := fragments.Validate(asset)
	rep := &VerifyReport{
		Asset: asset, ElementCount: asset.ElementCount(), TrackCount: len(asset.Tracks),
		FragmentCount: len(asset.Fragments), MaxDeviation: frag.MaxDeviation,
		DeviationAt: frag.DeviationAt, Tracks: asset.Tracks, Issues: issues,
		Fragments: frag, SHA256: asset.SHA256,
	}
	if err := rep.Err(); err != nil {
		return &MigrateOutcome{Verify: rep}, err
	}
	res, err := migrate.Run(migrate.Options{
		Out: opts.Out, StopAfter: opts.StopAfter, Resume: opts.Resume,
		Manifest: m, Asset: asset,
	})
	if err != nil {
		return &MigrateOutcome{Verify: rep}, err
	}
	return &MigrateOutcome{Verify: rep, Result: res}, nil
}

// DescribeElements renders the first n element records with raw offsets, for
// auditing a failure against a hex dump.
func DescribeElements(a *media.Asset, n int) string {
	var b bytes.Buffer
	for i, e := range a.Elements {
		if n > 0 && i >= n {
			break
		}
		fmt.Fprintf(&b, "  %-24s id=0x%-8X offset=%d size=%d\n", e.Name, e.ID, e.Offset, e.Size)
	}
	return b.String()
}

// ElementSummary counts known Matroska IDs for the report header.
func ElementSummary(a *media.Asset) string {
	return fmt.Sprintf("%d elements across %d unknown-length masters", a.ElementCount(), a.UnknownSized)
}
