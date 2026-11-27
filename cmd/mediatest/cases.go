package main

import (
	"fmt"
	"os"
	"path/filepath"

	"medialib/internal/archive"
	merr "medialib/internal/errors"
	"medialib/internal/fingerprint"
	"medialib/internal/ingest"
)

func printReport(r *report) {
	fmt.Printf("rate=%d channels=%d peak_dbfs=%.2f align_error_samples=%d\n",
		r.rate, r.channels, r.peakDBFS, r.alignError)
	fmt.Printf("segment_hash=%s\n", r.segmentHash)
	fmt.Printf("frame_hash=%s\n", r.frameHash)
	fmt.Printf("pixels=min:%d,max:%d,mean:%d,nonzero:%d\n", r.pxMin, r.pxMax, r.pxMean, r.pxNZ)
	fmt.Printf("duplicates=%d lost=%d limiter_engaged=%v reused=%v\n",
		r.duplicates, r.lost, r.limiter, r.reused)
}

// caseNormal runs the full pipeline on the well-formed fixture with
// one duplicate and two lost packets, plus MP4 index validation.
func caseNormal(p params) int {
	spec := defaultSpec()
	mp4Good, err := ingest.GenMP4(ingest.MP4Spec{
		Timescale: 48000, SampleRate: 48000, Channels: 2,
		Samples: 64, SampleBytes: 24, Delta: 1024, Seed: 11,
	})
	if err != nil {
		fmt.Println("error:", err)
		return 2
	}
	info, err := ingest.ParseMP4(mp4Good)
	if err != nil {
		fmt.Println("error:", err)
		return 2
	}
	fmt.Printf("mp4_timescale=%d mp4_samples=%d mp4_channels=%d\n",
		info.Timescale, info.SampleCount, info.Channels)
	r, err := runPipeline(filepath.Join(p.outDir, "normal"), spec, p)
	if err != nil {
		fmt.Println("error:", err)
		return 2
	}
	printReport(r)
	return 0
}

// caseCorrupt feeds truncated and bad-index MP4 fixtures (each must
// fail with stage and offset) and verifies the archive holds no
// uncommitted artifacts. Exit code 2: input failures occurred.
func caseCorrupt(p params) int {
	code := 2
	for _, sub := range []struct {
		name string
		kind ingest.CorruptKind
	}{
		{"truncate", ingest.CorruptTruncate},
		{"badindex", ingest.CorruptBadIndex},
	} {
		data, err := ingest.GenMP4(ingest.MP4Spec{
			Timescale: 48000, SampleRate: 48000, Channels: 2,
			Samples: 64, SampleBytes: 24, Delta: 1024, Seed: 11, Corrupt: sub.kind,
		})
		if err != nil {
			fmt.Println("error:", err)
			continue
		}
		_, perr := ingest.ParseMP4(data)
		if perr == nil {
			fmt.Printf("scenario=%s error=expected corruption not detected\n", sub.name)
			continue
		}
		stage, _ := merr.StageOf(perr)
		off, _ := merr.OffsetOf(perr)
		fmt.Printf("scenario=%s error=%q stage=%s offset=%d\n", sub.name, perr, stage, off)
	}
	// Corrupt input must never overwrite a valid archive.
	dir := filepath.Join(p.outDir, "corrupt-guard")
	good, err := runPipeline(dir, defaultSpec(), p)
	if err != nil {
		fmt.Println("error:", err)
		return 2
	}
	badNorm := fingerprint.HashBytes([]byte("corrupt-payload"))
	store, err := archive.OpenStore(dir)
	if err != nil {
		fmt.Println("error:", err)
		return 2
	}
	cerr := func() error {
		defer store.Close()
		_, err := store.Commit("audio", "segment.pcm", []byte("corrupt-payload"), badNorm+"00")
		return err
	}()
	if cerr == nil {
		fmt.Println("scenario=guard error=corrupt commit accepted")
	} else if ok, _ := store.Verify("segment.pcm", good.archiveHash); !ok {
		fmt.Println("scenario=guard error=valid archive overwritten")
	} else {
		fmt.Printf("scenario=guard rejected=%q archive_intact=1\n", cerr)
	}
	fmt.Printf("archive_clean=%v\n", noTempFiles(p.outDir))
	return code
}

// caseInterrupt simulates a crash between journal begin and commit,
// recovers, and checks the rerun reproduces identical fingerprints.
func caseInterrupt(p params) int {
	dir := filepath.Join(p.outDir, "interrupt")
	spec := defaultSpec()
	// First attempt "crashes" after the audio stage began: the begin
	// record and temp file exist, the commit never happened.
	first, err := runPipeline(dir, spec, p)
	if err != nil {
		fmt.Println("error:", err)
		return 2
	}
	crash, err := archive.OpenStore(dir)
	if err != nil {
		fmt.Println("error:", err)
		return 2
	}
	if err := crash.WriteUncommitted("audio", "partial.pcm", []byte("half-written stage")); err != nil {
		fmt.Println("error:", err)
		return 2
	}
	crash.Close()
	// Recovery: uncommitted temp rolls back, rerun from last boundary.
	rec, err := archive.OpenStore(dir)
	if err != nil {
		fmt.Println("error:", err)
		return 2
	}
	rollbacks := rec.RolledBack
	rec.Close()
	second, err := runPipeline(dir, spec, p)
	if err != nil {
		fmt.Println("error:", err)
		return 2
	}
	match := first.segmentHash == second.segmentHash && first.frameHash == second.frameHash
	fmt.Printf("scenario=interrupt exit_code=0 rollbacks=%d rerun_hash_match=%v last_committed=%s\n",
		rollbacks, match, lastCommitted(dir))
	if rollbacks == 0 || !match {
		return 2
	}
	return 0
}

func noTempFiles(dir string) bool {
	clean := true
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(path) == ".tmp" {
			clean = false
		}
		return nil
	})
	return clean
}

func lastCommitted(dir string) string {
	s, err := archive.OpenStore(dir)
	if err != nil {
		return ""
	}
	defer s.Close()
	return s.LastCommitted()
}
