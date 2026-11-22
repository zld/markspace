package app

import (
	"os"
	"path/filepath"

	"mediatimeline/internal/clock"
	"mediatimeline/internal/container"
)

// RunCorrupt generates the standard fixture, applies the requested damage and
// runs the scanner/pipeline far enough to localize it. Recoverable damage is
// reported with file offset, track id and sample index, accurate to one sample.
func RunCorrupt(dir, kind string) (DamageLocation, error) {
	spec := fixtureSpec(7)
	clean, err := container.BuildMP4(spec)
	if err != nil {
		return DamageLocation{}, err
	}

	var damaged []byte
	switch kind {
	case "truncated-stbl":
		damaged, err = container.TruncateSTBL(clean)
	case "illegal-offset":
		damaged = corruptOffset(clean)
	case "bad-crc":
		damaged, _, err = container.CorruptCRC(clean, sampleCount/2)
	case "checkpoint":
		return corruptCheckpoint(dir)
	default:
		return DamageLocation{}, clock.NewError(clock.ErrInvariant, "unknown corruption kind: "+kind)
	}
	if err != nil {
		return DamageLocation{}, err
	}

	path := filepath.Join(dir, "damaged.m4a")
	if err := os.WriteFile(path, damaged, 0o600); err != nil {
		return DamageLocation{}, err
	}

	sc, err := container.Open(path)
	if err != nil {
		return DamageLocation{}, err
	}
	defer sc.Close()
	tracks, err := sc.ScanTracks()
	if err != nil {
		// Structural damage (truncated box / illegal offset) surfaces here with
		// the position already attached by the scanner.
		if ce, ok := err.(*clock.Error); ok {
			return DamageLocation{Offset: ce.FileOffset, TrackID: ce.TrackID, SampleIndex: ce.SampleIndex}, err
		}
		return DamageLocation{}, err
	}

	// Payload damage: attach the trusted clean CRCs, then run the pipeline so
	// the CRC check localizes the exact sample.
	cleanPath := filepath.Join(dir, "clean.m4a")
	if err := os.WriteFile(cleanPath, clean, 0o600); err != nil {
		return DamageLocation{}, err
	}
	csc, err := container.Open(cleanPath)
	if err != nil {
		return DamageLocation{}, err
	}
	cleanTracks, err := csc.ScanTracks()
	csc.Close()
	if err != nil {
		return DamageLocation{}, err
	}
	container.AttachTrustedCRC(tracks[0].Entries, cleanTracks[0].Entries)

	src, err := os.Open(path)
	if err != nil {
		return DamageLocation{}, err
	}
	defer src.Close()
	cfg := pipelineConfig(DemoOptions{Seed: 7, CheckpointEvery: 0})
	p, err := newPipelineForDamage(cfg, dir, tracks[0].Entries, src)
	if err != nil {
		return DamageLocation{}, err
	}
	res, err := p.Run(tracks[0].Entries, 0)
	if len(res.Skipped) > 0 {
		ce := res.Skipped[0]
		return DamageLocation{Offset: ce.FileOffset, TrackID: ce.TrackID, SampleIndex: ce.SampleIndex}, ce
	}
	if err != nil {
		return DamageLocation{}, err
	}
	return DamageLocation{}, clock.NewError(clock.ErrInvariant, "corruption not detected")
}

// corruptOffset points the first stco chunk beyond the end of the file.
func corruptOffset(mp4Bytes []byte) []byte {
	idx, _, err := findStco(mp4Bytes)
	if err != nil {
		return mp4Bytes
	}
	out := append([]byte(nil), mp4Bytes...)
	// stco payload: 4 version/flags + 4 count + first entry.
	pos := idx + 8 + 4 + 4
	out[pos] = 0x7F
	out[pos+1] = 0xFF
	out[pos+2] = 0xFF
	out[pos+3] = 0xFF
	return out
}

func findStco(data []byte) (int, int, error) {
	return container.FindBoxForTest(data, "stco")
}
