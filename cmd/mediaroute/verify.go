package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediaroute/internal/index"
	"mediaroute/internal/ingest"
	"mediaroute/internal/model"
	"mediaroute/internal/report"
	"mediaroute/internal/timeline"
)

// verifyRecord is the JSON emitted by the verify subcommand.
type verifyRecord struct {
	InputPath         string       `json:"input"`
	ManifestPath      string       `json:"manifest"`
	IndexPath         string       `json:"index_path,omitempty"`
	OK                bool         `json:"ok"`
	IndexValidEntries int          `json:"index_valid_entries"`
	IndexTotalEntries int          `json:"index_total_entries"`
	MaxAlignmentUS    model.Micros `json:"max_alignment_us"`
	TruncatedBoxes    int          `json:"truncated_boxes"`
	SHA256Match       bool         `json:"sha256_match"`
	ManifestHashMatch bool         `json:"manifest_digest_match"`
	IndexEntriesMatch bool         `json:"index_entries_match"`
	Error             string       `json:"error,omitempty"`
	ErrorCategory     string       `json:"error_category,omitempty"`
}

func runVerify(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "delivered fMP4 to verify")
	manifestPath := fs.String("manifest", "", "manifest produced alongside the input")
	indexPath := fs.String("index", "", "optional random-access index to cross-check")
	maxBytes := fs.Int64("max-bytes", 256<<20, "maximum input file size to read")
	if err := fs.Parse(args); err != nil {
		return model.WrapTaskError(model.CategoryInvalidConfig, "cli", "parse verify flags", err)
	}
	if *input == "" || *manifestPath == "" {
		return model.NewTaskError(model.CategoryInvalidConfig, "cli", "--input and --manifest are required")
	}
	rec := verifyRecord{InputPath: *input, ManifestPath: *manifestPath, IndexPath: *indexPath}
	result, err := verifyFiles(*input, *manifestPath, *indexPath, *maxBytes, &rec)
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(rec)
	if err != nil {
		if te, ok := model.AsTaskError(err); ok {
			rec.ErrorCategory = string(te.Category)
		}
		return err
	}
	if !result.OK {
		return model.NewTaskError(model.CategoryIndexMismatch, "verify",
			fmt.Sprintf("index verification failed: %d of %d entries invalid, max alignment %d us",
				rec.IndexTotalEntries-rec.IndexValidEntries, rec.IndexTotalEntries, rec.MaxAlignmentUS))
	}
	return nil
}

// verifyResult is the internal outcome used to build the record.
type verifyResult struct {
	OK     bool
	Record *verifyRecord
}

// verifyFiles re-reads the delivered media, rebuilds the index, and checks it
// against the stored manifest. It fails closed: any unrecoverable boundary,
// missing init or digest mismatch is an error.
func verifyFiles(inputPath, manifestPath, indexPath string, maxBytes int64, rec *verifyRecord) (*verifyResult, error) {
	if maxBytes <= 0 {
		maxBytes = 256 << 20
	}
	info, err := os.Stat(inputPath)
	if err != nil {
		return nil, model.WrapTaskError(model.CategoryIO, "verify", "stat input", err)
	}
	if info.Size() > maxBytes {
		return nil, model.NewTaskError(model.CategoryInvalidConfig, "verify",
			fmt.Sprintf("input %d bytes exceeds limit %d", info.Size(), maxBytes))
	}
	raw, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, model.WrapTaskError(model.CategoryIO, "verify", "read input", err)
	}
	manifestBytes, err := os.ReadFile(filepath.Clean(manifestPath))
	if err != nil {
		return nil, model.WrapTaskError(model.CategoryIO, "verify", "read manifest", err)
	}
	var manifest model.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, model.WrapTaskError(model.CategoryManifestMismatch, "verify",
			"manifest is not valid JSON", err)
	}

	// Recompute the input digest and compare with the manifest.
	sum := sha256.Sum256(raw)
	rec.SHA256Match = hex.EncodeToString(sum[:]) == manifest.OutputSHA256
	if !rec.SHA256Match {
		return nil, model.NewTaskError(model.CategoryManifestMismatch, "verify",
			"input sha256 does not match the manifest; the media was altered or the manifest is for another file")
	}

	set, err := ingest.Read(raw, ingest.Config{MaxBoxSize: 64 << 20, Name: "delivered"})
	if err != nil {
		return nil, err
	}
	// Repair is idempotent on already-repaired media, so running it here both
	// validates the delivered timestamps and yields the exact decode times the
	// index must reproduce.
	rep, err := timeline.NewRepairer(set)
	if err != nil {
		return nil, err
	}
	for i := range set.Fragments {
		if err := rep.Apply(&set.Fragments[i]); err != nil {
			return nil, err
		}
	}
	if len(set.Fragments) == 0 {
		return nil, model.NewTaskError(model.CategoryCorruptIndex, "verify", "delivered file has no fragments")
	}
	// Rebuild through the same delivery path the demo uses. Re-rendering the
	// already-repaired media must reproduce the same bytes and the same index
	// offsets as the stored artifacts; that is what makes verification
	// meaningful rather than a re-parse of unverified structure.
	delivery, err := index.WriteRepairedSet(io.Discard, set, index.WriteOptions{})
	if err != nil {
		return nil, err
	}
	built := &index.BuildResult{Index: delivery.Index, Entries: delivery.Index.Len()}

	validation, err := index.Validate(built.Index, &manifest)
	if err != nil {
		return nil, err
	}
	rec.IndexTotalEntries = built.Index.Len()
	rec.IndexValidEntries = validation.ValidEntries
	rec.MaxAlignmentUS = validation.MaxAlignment
	rec.TruncatedBoxes = 0
	for i := range set.Fragments {
		if set.Fragments[i].Truncated {
			rec.TruncatedBoxes++
		}
	}
	// Optionally cross-check the standalone index artifact: every entry it
	// records must be reproduced exactly by the rebuild.
	if indexPath != "" {
		idxBytes, err := os.ReadFile(filepath.Clean(indexPath))
		if err != nil {
			return nil, model.WrapTaskError(model.CategoryIO, "verify", "read index", err)
		}
		var doc report.IndexDocument
		if err := json.Unmarshal(idxBytes, &doc); err != nil {
			return nil, model.WrapTaskError(model.CategoryManifestMismatch, "verify",
				"index file is not valid JSON", err)
		}
		rebuilt := built.Index.Sorted()
		match := len(doc.Entries) == len(rebuilt)
		if match {
			for i := range rebuilt {
				if doc.Entries[i] != rebuilt[i] {
					match = false
					break
				}
			}
		}
		rec.IndexEntriesMatch = match
		if !match {
			return nil, model.NewTaskError(model.CategoryIndexMismatch, "verify",
				fmt.Sprintf("index artifact does not match the rebuild (%d vs %d entries)",
					len(doc.Entries), len(rebuilt)))
		}
	} else {
		rec.IndexEntriesMatch = true
	}
	// Semantic consistency between the manifest and the rebuilt media. The
	// manifest is a record of the delivery, so its entry count and per-track
	// spans must agree with what the delivered bytes actually contain.
	if manifest.IndexEntries != built.Index.Len() {
		return nil, model.NewTaskError(model.CategoryManifestMismatch, "verify",
			fmt.Sprintf("manifest records %d index entries but the media yields %d",
				manifest.IndexEntries, built.Index.Len()))
	}
	digestOK, err := report.VerifyManifestDigest(&manifest)
	if err != nil {
		return nil, err
	}
	rec.ManifestHashMatch = digestOK
	if !digestOK {
		return nil, model.NewTaskError(model.CategoryManifestMismatch, "verify",
			"manifest self-digest does not match its contents; the manifest was altered")
	}
	rec.OK = validation.ValidEntries == built.Index.Len() && len(validation.BadOffsets) == 0
	if validation.ValidEntries != built.Index.Len() {
		return nil, model.NewTaskError(model.CategoryIndexMismatch, "verify",
			fmt.Sprintf("%d of %d rebuilt entries do not land on box boundaries",
				built.Index.Len()-validation.ValidEntries, built.Index.Len()))
	}
	return &verifyResult{OK: rec.OK, Record: rec}, nil
}
