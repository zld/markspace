package main

import (
	"assetcheck/internal/atomicfile"
	"assetcheck/internal/cache"
	"assetcheck/internal/fixture"
	"assetcheck/internal/wav"
)

func fingerprintsOf(data []byte, w *wav.Wav) cache.Fingerprints {
	return cache.Fingerprints{
		Content:   wav.ContentFingerprint(w),
		Structure: wav.StructureFingerprint(data, w),
		Metadata:  wav.MetadataFingerprint(data, w),
	}
}

// runMutation isolates a single change axis: force a known baseline cache
// entry, apply the deterministic mutation to the same path, then compare the
// new fingerprints so exactly the affected cache object is reported.
func runMutation(absPath, kind string, data []byte, c *cache.Store) ([]byte, []cache.Invalidation, error) {
	w0, err := wav.Parse(data)
	if err != nil {
		return nil, nil, err
	}
	baseline := cache.Entry{Path: absPath, Fingerprints: fingerprintsOf(data, w0)}
	if err := c.ReplaceBaseline(baseline); err != nil {
		return nil, nil, err
	}

	var next []byte
	switch kind {
	case "content":
		next, err = fixture.MutateContent(data)
	case "container":
		next, err = fixture.MutateContainer(data)
	case "metadata":
		next, err = fixture.MutateMetadata(data)
	default:
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if err := atomicfile.WriteFile(absPath, next, 0o644); err != nil {
		return nil, nil, err
	}

	w1, err := wav.Parse(next)
	if err != nil {
		return nil, nil, err
	}
	inv := cache.Check(baseline, true, fingerprintsOf(next, w1))
	return next, inv, nil
}
