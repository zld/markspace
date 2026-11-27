package pipeline

import (
	"os"
	"path/filepath"

	"mediacore/internal/archive"
	"mediacore/internal/clock"
	"mediacore/internal/fault"
	"mediacore/internal/media"
)

// stageArchive packages the fragment, confirms its timing with go-mp4, and
// commits it idempotently by source digest.
func stageArchive(tool *media.Tool, rep *Report, opts Options, syn media.Synthetic, encoded, wav string, rawBytes, recoveredBytes []byte) error {
	mediaDir := filepath.Dir(encoded)
	mp4 := filepath.Join(mediaDir, "archive_"+string(opts.Codec)+".mp4")
	if err := tool.Remux(encoded, wav, mp4, string(opts.Codec), syn); err != nil {
		return err
	}
	probe, err := tool.ProbeMP4(mp4)
	if err != nil {
		return err
	}
	rep.Container = *probe
	// Confirm the fragment with go-mp4 independently of the encoder. AAC
	// pads its final frame, so a sound track may run slightly long; that
	// legal drift is tolerated by archive.ValidateFragment and reported.
	timing, err := archive.ValidateFragment(mp4, clock.Time(syn.Seconds)*clock.Second, 30*clock.Millisecond)
	if err != nil {
		return err
	}
	rep.ContainerDriftMS = round3(fragmentDriftMS(timing, syn))
	store, err := archive.Open(filepath.Join(opts.Dir, "archive"))
	if err != nil {
		return err
	}
	blob, err := os.ReadFile(mp4)
	if err != nil {
		return fault.Newf(fault.CodeInternal, "", "cannot read archive fragment: %v", err)
	}
	psnr := rep.PSNRdB
	entry := archive.Entry{
		Digest:     archive.DigestOf(rawBytes),
		BlobDigest: archive.DigestOf(blob),
		Codec:      string(opts.Codec),
		PSNRdB:     psnr,
		Verified:   true,
	}
	stored, existed, err := store.Commit(entry, blob)
	if err != nil {
		return err
	}
	rep.ArchiveDigest = stored.Digest
	// Commit reports a duplicate when the identical source digest was
	// already archived, so a repeat delivery never appends a second record.
	rep.ArchiveDuplicate = existed
	return os.WriteFile(filepath.Join(opts.Dir, "archive", "recovered_"+string(opts.Codec)+".bin"), recoveredBytes, 0o644)
}
