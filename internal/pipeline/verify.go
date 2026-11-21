package pipeline

import (
	"bytes"
	"fmt"
	"path/filepath"

	"hlsflow/internal/config"
	"hlsflow/internal/integrity"
	"hlsflow/internal/m3u8"
	"hlsflow/internal/safeio"
)

// Verify re-parses the published master playlist from disk (it does not trust
// build-time state), then checks every referenced file's existence, SHA-256
// and EXTINF duration against the integrity index and the media timeline.
// Orphaned files in the output tree are reported as extras.
func Verify(job *config.Job, outputRoot *safeio.Root) (*Report, error) {
	masterData, err := outputRoot.ReadFile("master.m3u8")
	if err != nil {
		return nil, fmt.Errorf("read master playlist: %w", err)
	}
	variants, err := m3u8.ParseMaster(bytes.NewReader(masterData))
	if err != nil {
		return nil, fmt.Errorf("parse master playlist: %w", err)
	}
	index, err := integrity.Load(outputRoot)
	if err != nil {
		return nil, fmt.Errorf("load integrity index: %w", err)
	}

	report := &Report{job: job}
	referenced := map[string]bool{"master.m3u8": true, integrity.IndexName: true}

	for _, variant := range variants {
		rd := job.Rendition(variantName(variant))
		if rd == nil {
			return nil, fmt.Errorf("master references unknown rendition %q", variant.URI)
		}
		line := report.ensureRendition(*rd)
		plRel := variant.URI
		referenced[plRel] = true
		plData, err := outputRoot.ReadFile(plRel)
		if err != nil {
			line.Failures = append(line.Failures, Failure{
				Rendition: rd.Name, Reason: "media playlist missing: " + plRel,
			})
			continue
		}
		parsed, perr := m3u8.ParseMedia(bytes.NewReader(plData))
		if perr != nil {
			line.Failures = append(line.Failures, Failure{
				Rendition: rd.Name, Reason: "media playlist unparseable: " + perr.Error(),
			})
			continue
		}
		verifyEntries(report, line, rd, parsed, index, outputRoot, referenced)
	}

	extras, err := findExtras(outputRoot, referenced)
	if err != nil {
		return nil, err
	}
	report.ExtraFiles = extras
	if len(extras) > 0 {
		// Extras are reported per rendition-independent line; attach to first.
		if len(report.Renditions) > 0 {
			report.Renditions[0].Failures = append(report.Renditions[0].Failures,
				Failure{Reason: "unreferenced files present: " + fmt.Sprint(extras)})
		}
	}
	applyVerifyDrift(report, index, job)
	return report, nil
}

// applyVerifyDrift recomputes cross-rendition boundary drift entirely from the
// reloaded integrity index, so verify does not depend on build-time memory.
func applyVerifyDrift(report *Report, index *integrity.Index, job *config.Job) {
	if len(job.Renditions) < 2 {
		return
	}
	refName := job.Renditions[0].Name
	ref := index.Renditions[refName].Entries
	tolerance := job.DriftTolerance.Duration.Seconds()
	for i := 1; i < len(job.Renditions); i++ {
		name := job.Renditions[i].Name
		entries := index.Renditions[name].Entries
		var maxDrift float64
		var atSeg int
		n := len(ref)
		if len(entries) < n {
			n = len(entries)
		}
		for j := 0; j < n; j++ {
			d := entries[j].BoundarySeconds - ref[j].BoundarySeconds
			if absFloat(d) > absFloat(maxDrift) {
				maxDrift, atSeg = d, entries[j].Segment
			}
		}
		for k := range report.Renditions {
			if report.Renditions[k].Rendition != name {
				continue
			}
			report.Renditions[k].DriftSeconds = round3(maxDrift)
			if tolerance > 0 && absFloat(maxDrift) > tolerance {
				report.Renditions[k].Failures = append(report.Renditions[k].Failures, Failure{
					Rendition: name, Segment: atSeg,
					Reason: fmt.Sprintf("timeline drift %.3fs exceeds tolerance %.3fs", maxDrift, tolerance),
				})
			}
		}
	}
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func verifyEntries(
	report *Report,
	line *RenditionReport,
	rd *config.Rendition,
	parsed *m3u8.ParsedMedia,
	index *integrity.Index,
	root *safeio.Root,
	referenced map[string]bool,
) {
	entries := index.Renditions[rd.Name].Entries
	bySegment := map[int]integrity.Entry{}
	for _, e := range entries {
		bySegment[e.Segment] = e
	}
	var timelineTotal float64
	for i, seg := range parsed.Segments {
		mediaSeq := parsed.MediaSeqOf(i)
		rel := filepath.Join(rd.Name, seg.URI)
		referenced[rel] = true
		entry, ok := bySegment[mediaSeq]
		if !ok {
			line.Failures = append(line.Failures, Failure{
				Rendition: rd.Name, Segment: mediaSeq, Reason: "no integrity entry for playlist segment",
			})
			continue
		}
		// Faults are applied only to the bytes read for hashing, in memory.
		data, err := integrity.OpenSegment(root, report.job, rd.Name, rel, mediaSeq)
		if err != nil {
			line.Failures = append(line.Failures, Failure{
				Rendition: rd.Name, Segment: mediaSeq, Reason: "segment file missing: " + seg.URI,
			})
			continue
		}
		got := integrity.HashBytes(data)
		if got != entry.SHA256 {
			line.ChecksumFailures++
			line.Failures = append(line.Failures, Failure{
				Rendition: rd.Name, Segment: mediaSeq, Reason: "sha256 mismatch",
			})
		}
		if d := seg.Duration - entry.Duration; d > 0.05 || d < -0.05 {
			line.Failures = append(line.Failures, Failure{
				Rendition: rd.Name, Segment: mediaSeq,
				Reason: fmt.Sprintf("EXTINF %.3f disagrees with media timeline %.3f", seg.Duration, entry.Duration),
			})
		}
		timelineTotal += entry.Duration
	}
	line.Segments = len(parsed.Segments)
	line.DurationSeconds = round3(timelineTotal)
}

func variantName(v m3u8.Variant) string {
	if v.Name != "" {
		return v.Name
	}
	// Fall back to the directory component of the URI.
	dir := filepath.Dir(v.URI)
	return filepath.Base(dir)
}

func findExtras(root *safeio.Root, referenced map[string]bool) ([]string, error) {
	all, err := root.ListFiles(".")
	if err != nil {
		return nil, err
	}
	var extras []string
	for _, f := range all {
		if !referenced[f] {
			extras = append(extras, f)
		}
	}
	return extras, nil
}
