package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"tsindex/internal/fixture"
	"tsindex/internal/index"
	"tsindex/internal/verify"
)

func parseVerifyFlags(args []string) (string, error) {
	root := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--root":
			if i+1 >= len(args) {
				return "", errors.New("--root needs a value")
			}
			root = args[i+1]
			i++
		default:
			return "", fmt.Errorf("unknown flag: %s", args[i])
		}
	}
	if root == "" {
		return "", errors.New("--root is required")
	}
	return root, nil
}

func runVerify(args []string) error {
	root, err := parseVerifyFlags(args)
	if err != nil {
		return err
	}
	m, err := readManifest(root)
	if err != nil {
		return err
	}

	// Sidecars are named "<asset>-segment-N.ts.tsix"; group them by asset so
	// each source is re-hashed only against its own segment boundaries.
	allSidecars, err := filepath.Glob(filepath.Join(root, "*-segment-*.ts.tsix"))
	if err != nil {
		return err
	}
	recordsByAsset := map[string][]index.SegmentRecord{}
	incompleteSidecars := 0
	for _, p := range allSidecars {
		rec, rerr := index.ReadSidecar(p)
		if rerr != nil {
			if errors.Is(rerr, index.ErrIncompleteSidecar) {
				incompleteSidecars++
				fmt.Printf("incomplete_sidecar=%s status=recoverable\n", filepath.Base(p))
				continue
			}
			return rerr
		}
		base := filepath.Base(p)
		asset := base
		if idx := strings.Index(base, "-segment-"); idx >= 0 {
			asset = base[:idx]
		}
		recordsByAsset[asset] = append(recordsByAsset[asset], rec)
	}

	totalSeg, matchedSeg := 0, 0
	refKF, refHit := 0, 0
	unrecoverable := false
	var faultLines []string

	for _, asset := range m.Assets {
		sourcePath := filepath.Join(root, asset.Source)
		// Distinct validators keep framing faults and segment faults from
		// being double-counted through a shared fault recorder.
		frameV := verify.NewValidator()
		segV := verify.NewValidator()
		// Framing pass: truncation, sync bytes, CC gaps and time monotonicity.
		frameReport, ferr := frameV.VerifyFile(sourcePath, nil)
		if ferr != nil {
			return ferr
		}
		// Segment pass: re-hash this asset's sidecar boundaries from source.
		segReport, serr := segV.VerifySegments(sourcePath, recordsByAsset[asset.Name])
		if serr != nil {
			return serr
		}
		totalSeg += segReport.Segments
		matchedSeg += segReport.SegmentsMatched
		// The headline keyframe ratio is anchored to the intact reference
		// asset so deliberately damaged fixtures cannot move the contract.
		if asset.Name == fixture.CaseNormal {
			refKF += segReport.Keyframes
			refHit += segReport.KeyframesHit
		}
		for _, flt := range frameReport.Faults {
			faultLines = append(faultLines,
				fmt.Sprintf("%s offset=%d recoverable=%v kind=%s detail=%q",
					asset.Name, flt.Offset, flt.Recoverable, flt.Kind, flt.Detail))
			if !flt.Recoverable {
				unrecoverable = true
			}
		}
		for _, flt := range segReport.Faults {
			faultLines = append(faultLines,
				fmt.Sprintf("%s offset=%d recoverable=%v kind=%s detail=%q",
					asset.Name, flt.Offset, flt.Recoverable, flt.Kind, flt.Detail))
			if !flt.Recoverable {
				unrecoverable = true
			}
		}
		if !frameReport.Recoverable || !segReport.Recoverable {
			unrecoverable = true
		}
	}

	roundtrip := 0
	if totalSeg > 0 {
		roundtrip = matchedSeg * 100 / totalSeg
	}
	fmt.Printf("roundtrip=%d%%\n", roundtrip)
	fmt.Printf("keyframe_hit=%d/%d\n", refHit, refKF)
	fmt.Printf("segment_sha256_match=%d/%d\n", matchedSeg, totalSeg)
	fmt.Printf("incomplete_sidecars=%d\n", incompleteSidecars)
	for _, line := range faultLines {
		fmt.Println("fault " + line)
	}
	if unrecoverable {
		return errors.New("verification found an unrecoverable fault")
	}
	if len(faultLines) > 0 {
		fmt.Printf("status=ok recoverable_faults=%d\n", len(faultLines))
	} else {
		fmt.Println("status=ok")
	}
	return nil
}
