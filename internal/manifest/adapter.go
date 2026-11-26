package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"continuity/internal/atomicfile"
	"continuity/internal/media"
)

// AdaptOptions controls version adaptation and durable rollback points.
type AdaptOptions struct {
	// RollbackDir receives one atomic snapshot before every migration stage.
	RollbackDir string
}

// AdaptFile reads path, migrates it in memory, and returns the canonical
// manifest. It never overwrites path; callers atomically publish the result.
func AdaptFile(path string, opts AdaptOptions) (*Manifest, *MigrationReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, media.NewError(media.StageManifest, 0, path, err).WithKind("read_manifest")
	}
	m, report, err := AdaptBytes(data, opts)
	if err != nil {
		return nil, report, err
	}
	return m, report, nil
}

// AdaptBytes migrates JSON bytes through every required major-version stage.
func AdaptBytes(input []byte, opts AdaptOptions) (*Manifest, *MigrationReport, error) {
	root, err := decodeObject(input)
	if err != nil {
		return nil, nil, media.NewError(media.StageManifest, -1, "manifest", err).WithKind("json_decode")
	}
	version, major, err := detectVersion(root)
	if err != nil {
		return nil, nil, err
	}
	report := &MigrationReport{OriginalVersion: version}

	switch major {
	case 3:
		report.FinalVersion = CanonicalVersion
		report.Stages = append(report.Stages, "already_canonical")
	case 1:
		if err := saveRollback(opts, report, "stage-01-before-v1-v2.json", root); err != nil {
			return nil, report, err
		}
		root, err = migrateV1ToV2(root)
		if err != nil {
			return nil, report, err
		}
		report.Stages = append(report.Stages, "v1_to_v2")
		version = "2.0.0"
		fallthrough
	case 2:
		if major == 2 {
			if err := saveRollback(opts, report, "stage-02-before-v2-v3.json", root); err != nil {
				return nil, report, err
			}
		} else if err := saveRollback(opts, report, "stage-02-before-v2-v3.json", root); err != nil {
			return nil, report, err
		}
		root, err = migrateV2ToV3(root)
		if err != nil {
			return nil, report, err
		}
		report.Stages = append(report.Stages, "v2_to_v3")
		version = CanonicalVersion
	default:
		return nil, report, media.NewError(media.StageManifest, -1, "schema_version", media.ErrUnsupportedVersion).
			WithKind(fmt.Sprintf("major_%d", major))
	}

	root["schema_version"] = CanonicalVersion
	canonicalJSON, err := json.Marshal(root)
	if err != nil {
		return nil, report, media.NewError(media.StageManifest, -1, "manifest", media.ErrMigration).WithKind("marshal_canonical")
	}
	var out Manifest
	dec := json.NewDecoder(bytes.NewReader(canonicalJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		// Unknown fields in old documents are tolerated during map migration;
		// strict decode failures indicate a failed semantic migration.
		var loose Manifest
		if looseErr := json.Unmarshal(canonicalJSON, &loose); looseErr != nil {
			return nil, report, media.NewError(media.StageManifest, -1, "manifest",
				fmt.Errorf("%w: %v", media.ErrMigration, err)).WithKind("canonical_decode")
		}
		out = loose
		report.Notes = append(report.Notes, "canonical decode used loose compatibility mode")
	}
	report.FinalVersion = out.SchemaVersion
	return &out, report, nil
}

// RestoreRollback atomically copies a stage snapshot back to destination.
func RestoreRollback(rollbackPath, destination string) error {
	data, err := os.ReadFile(rollbackPath)
	if err != nil {
		return err
	}
	return atomicfile.Write(destination, data, 0o644)
}

func decodeObject(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("manifest must be a JSON object")
	}
	return root, nil
}

func detectVersion(root map[string]any) (string, int, error) {
	for _, key := range []string{"schema_version", "manifest_version", "version"} {
		v, ok := root[key]
		if !ok {
			continue
		}
		switch x := v.(type) {
		case string:
			major, err := strconv.Atoi(strings.SplitN(x, ".", 2)[0])
			if err != nil {
				return x, 0, media.NewError(media.StageManifest, -1, key, media.ErrUnsupportedVersion).WithKind("bad_version")
			}
			return x, major, nil
		case json.Number:
			n, err := x.Int64()
			if err != nil {
				return x.String(), 0, media.NewError(media.StageManifest, -1, key, media.ErrUnsupportedVersion).WithKind("bad_version")
			}
			return x.String(), int(n), nil
		default:
			return "", 0, media.NewError(media.StageManifest, -1, key, media.ErrUnsupportedVersion).WithKind("bad_version_type")
		}
	}
	return "", 0, media.NewError(media.StageManifest, -1, "schema_version", media.ErrUnsupportedVersion).WithKind("missing_version")
}

func saveRollback(opts AdaptOptions, report *MigrationReport, name string, state map[string]any) error {
	if opts.RollbackDir == "" {
		return nil
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return media.NewError(media.StageManifest, -1, "rollback", media.ErrMigration).WithKind("marshal_snapshot")
	}
	path := filepath.Join(opts.RollbackDir, name)
	if err := atomicfile.Write(path, data, 0o644); err != nil {
		return media.NewError(media.StageManifest, -1, path, fmt.Errorf("%w: %v", media.ErrMigration, err)).
			WithKind("atomic_rollback_point")
	}
	report.RollbackPoints = append(report.RollbackPoints, path)
	return nil
}

func migrateV1ToV2(root map[string]any) (map[string]any, error) {
	if err := rejectConflict(root, "", "clips", "tracks"); err != nil {
		return nil, err
	}
	if clips, ok := root["clips"]; ok {
		root["tracks"] = clips
		delete(root, "clips")
	}
	if q, ok := root["quality"]; ok {
		if _, exists := root["constraints"]; exists && !jsonEqual(root["quality"], root["constraints"]) {
			return nil, conflictError(-1, "quality/constraints")
		}
		root["constraints"] = q
		delete(root, "quality")
	}
	delete(root, "manifest_version")
	root["schema_version"] = "2.0.0"
	return root, nil
}

func migrateV2ToV3(root map[string]any) (map[string]any, error) {
	rawTracks, ok := root["tracks"].([]any)
	if !ok {
		return nil, media.NewError(media.StageManifest, -1, "tracks", media.ErrMigration).WithKind("tracks_array")
	}
	for i, raw := range rawTracks {
		t, ok := raw.(map[string]any)
		if !ok {
			return nil, media.NewError(media.StageManifest, -1, fmt.Sprintf("tracks[%d]", i), media.ErrMigration).WithKind("track_object")
		}
		if err := normalizeAliases(t, fmt.Sprintf("tracks[%d]", i), "input_sample_rate_hz", "sample_rate", "rate"); err != nil {
			return nil, err
		}
		if err := normalizeAliases(t, fmt.Sprintf("tracks[%d]", i), "channels", "ch", "channel_count"); err != nil {
			return nil, err
		}
		if v, ok := t["layout_known"]; !ok {
			if layout, exists := t["layout"]; exists && layout != "" {
				t["layout_known"] = true
			} else {
				t["layout_known"] = false
			}
		} else {
			t["layout_known"] = truthy(v)
		}
		rawTracks[i] = t
	}
	root["tracks"] = rawTracks

	if c, ok := root["constraints"].(map[string]any); ok {
		if err := normalizeAliases(c, "constraints", "min_coverage_ppm", "coverage_min_ppm", "coverage_ppm_min"); err != nil {
			return nil, err
		}
		if err := normalizeAliases(c, "constraints", "max_av_skew_ms", "av_skew_ms", "max_skew_ms"); err != nil {
			return nil, err
		}
		if err := normalizeAliases(c, "constraints", "max_bitrate_bps", "bitrate_cap_bps"); err != nil {
			return nil, err
		}
		if err := normalizeAliases(c, "constraints", "budget_ms", "processing_budget_ms"); err != nil {
			return nil, err
		}
	}
	root["schema_version"] = CanonicalVersion
	return root, nil
}

func normalizeAliases(m map[string]any, path, canonical string, aliases ...string) error {
	canonicalValue, hasCanonical := m[canonical]
	for _, alias := range aliases {
		v, ok := m[alias]
		if !ok {
			continue
		}
		if hasCanonical && !jsonEqual(canonicalValue, v) {
			return conflictError(-1, path+"."+canonical+"/"+alias)
		}
		if !hasCanonical {
			m[canonical] = v
			canonicalValue = v
			hasCanonical = true
		}
		delete(m, alias)
	}
	return nil
}

func rejectConflict(m map[string]any, path string, keys ...string) error {
	var present []string
	for _, k := range keys {
		if _, ok := m[k]; ok {
			present = append(present, k)
		}
	}
	if len(present) > 1 {
		return conflictError(-1, strings.TrimPrefix(path+"/"+strings.Join(present, "+"), "/"))
	}
	return nil
}

func conflictError(offset int64, path string) error {
	return media.NewError(media.StageManifest, offset, path, media.ErrFieldConflict).WithKind("conflicting_fields")
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1" || x == "yes"
	case json.Number:
		n, _ := x.Int64()
		return n != 0
	default:
		return false
	}
}
