package main

import (
	"fmt"
	"path/filepath"

	"pwf/internal/fixture"
	"pwf/internal/media"
	"pwf/internal/pipeline"
	"pwf/internal/quality"
)

// buildHealthyFixtures generates the normal multi-camera material and
// returns the pipeline source descriptors.
func buildHealthyFixtures(dir string) ([]pipeline.Source, error) {
	opus1 := fixture.OpusStreamSpec("cam_a", 24)
	opus1.DropSeqs = []uint16{105} // one 1-packet gap
	opus1.DupSeqs = []uint16{110}  // one repeated packet
	if _, _, err := fixture.BuildRTPStream(dir, opus1); err != nil {
		return nil, err
	}

	opus2 := fixture.OpusStreamSpec("cam_b", 20)
	opus2.SSRC = 0x42420003
	if _, _, err := fixture.BuildRTPStream(dir, opus2); err != nil {
		return nil, err
	}

	aac := fixture.AACStreamSpec("cam_c", 18)
	if _, _, err := fixture.BuildRTPStream(dir, aac); err != nil {
		return nil, err
	}

	// One MP4/AAC track: 44.1 kHz, 1024-tick samples, 40-byte frames.
	mp4Path := filepath.Join(dir, "cam_d.mp4")
	sizes := make([]uint32, 16)
	for i := range sizes {
		sizes[i] = 40
	}
	if err := fixture.WriteMP4File(mp4Path, 44100, 1024, sizes); err != nil {
		return nil, err
	}

	return []pipeline.Source{
		{Track: "cam_a", Kind: pipeline.KindRTP, Path: filepath.Join(dir, "cam_a.rtp"),
			Codec: "opus", ClockHz: 48000},
		{Track: "cam_b", Kind: pipeline.KindRTP, Path: filepath.Join(dir, "cam_b.rtp"),
			Codec: "opus", ClockHz: 48000},
		{Track: "cam_c", Kind: pipeline.KindRTP, Path: filepath.Join(dir, "cam_c.rtp"),
			Codec: "aac", ClockHz: 44100},
		{Track: "cam_d", Kind: pipeline.KindMP4, Path: mp4Path, Codec: "aac"},
	}, nil
}

// buildDamagedFixture truncates a known RTP frame and runs the parser to
// recover the offset it reports. The fixture is laid out so the damaged
// frame's length prefix sits at byte offset 137 exactly: three leading
// frames of 45 bytes (3*45=135), a 2-byte separator, then frame four.
// A non-nil error carrying the exact offset is the expected outcome.
func buildDamagedFixture(dir string) (path string, expected, found int64, err error) {
	p, err := buildDamageFileAt137(dir)
	if err != nil {
		return "", 0, 0, err
	}
	// Damage the length prefix at byte 135; the parser then reports the
	// truncated packet data at its start offset 137.
	const prefixOffset int64 = 135
	const target int64 = 137
	if _, err := fixture.DamageAt(p, prefixOffset); err != nil {
		return "", 0, 0, err
	}
	_, perr := pipeline.Run(pipeline.Options{
		WorkDir: filepath.Join(dir, "work"),
		Sources: []pipeline.Source{{
			Track: "damaged", Kind: pipeline.KindRTP, Path: p, Codec: "opus", ClockHz: 48000,
		}},
		Thresholds: quality.DefaultThresholds(), Workers: 1,
	})
	if perr == nil {
		return p, target, -1, fmt.Errorf("damaged input did not fail parsing")
	}
	me, ok := media.AsError(perr)
	if !ok {
		return p, target, -1, fmt.Errorf("parser returned non-media error: %w", perr)
	}
	return p, target, me.Offset, nil
}

// buildDamageFileAt137 writes four valid length-prefixed Opus RTP frames
// sized so the fourth frame's length prefix is at byte 137.
func buildDamageFileAt137(dir string) (string, error) {
	// Frame on disk = 2-byte length + RTP packet. To make a 45-byte frame,
	// use a 43-byte RTP packet (12-byte header + 31-byte Opus payload).
	const frameLen = 45
	mk := func(seq uint16) []byte {
		pkt := fixture.RTPPacket(7, seq, uint32(seq)*960, 96, fixture.OpusPayload(12, 0, 31))
		if len(pkt) != frameLen-2 {
			panic(fmt.Sprintf("frame layout error: rtp packet %d bytes, want %d", len(pkt), frameLen-2))
		}
		return pkt
	}
	var packets [][]byte
	for seq := uint16(0); seq < 4; seq++ {
		packets = append(packets, mk(seq))
	}
	path := filepath.Join(dir, "damaged.rtp")
	if err := fixture.WriteRTPFile(path, packets); err != nil {
		return "", err
	}
	off, err := fixture.FindFrameStart(path, 3)
	if err != nil {
		return "", err
	}
	// Prefix of frame four at 135; its RTP data (and the reported damage
	// offset) begins at 137.
	if off != 135 {
		return "", fmt.Errorf("fixture layout error: frame 3 prefix at %d, want 135", off)
	}
	return path, nil
}

// runInfeasibleBudget forces transcode (bitrate over target) under a
// budget that cannot pay for it, and returns the first blocker name.
func runInfeasibleBudget(dir string) (string, error) {
	// 2000-byte AAC AU per 1024-tick frame at 44.1kHz => ~689 kbps, over
	// the 160 kbps target, so the planner demands a paid transcode.
	spec := fixture.AACStreamSpec("heavy", 12)
	payloads := make([][]byte, spec.Packets)
	for i := range payloads {
		payloads[i] = fixture.AACPayload(2000)
	}
	path, _, err := fixture.BuildRTPStreamPayloads(dir, spec, payloads)
	if err != nil {
		return "", err
	}
	_, perr := pipeline.Run(pipeline.Options{
		WorkDir: filepath.Join(dir, "work"),
		Sources: []pipeline.Source{{
			Track: "heavy", Kind: pipeline.KindRTP, Path: path, Codec: "aac", ClockHz: 44100,
		}},
		Thresholds: quality.TightBudget(), Workers: 1,
	})
	if perr == nil {
		return "", fmt.Errorf("infeasible budget unexpectedly succeeded")
	}
	// The plan node applies the budget gate and fails there.
	me, ok := media.AsError(perr)
	if !ok || me.Kind != media.KindPlanInfeasible {
		return "", fmt.Errorf("expected plan-infeasible failure, got: %w", perr)
	}
	return "plan", nil
}

// runInterruptAndResume derives a deterministic early node key from the
// completed run, performs a fresh interrupted run, then resumes it and
// reports the reused node count and the final manifest digest.
func runInterruptAndResume(dir string, sources []pipeline.Source) (string, int, int64, error) {
	full, err := pipeline.Run(pipeline.Options{
		WorkDir: filepath.Join(dir, "full"), Sources: sources,
		Thresholds: quality.DefaultThresholds(), Workers: 2,
	})
	if err != nil {
		return "", 0, 0, err
	}
	if len(full.Results) < 6 {
		return "", 0, 0, fmt.Errorf("graph too small to test interruption")
	}
	// Interrupt after the third node in stable topological order. By then
	// at least one complete ingest->measure chain (two nodes) is committed
	// and will be reused on resume, satisfying reused_nodes >= 2.
	stopKey := full.Results[2].Key

	intDir := filepath.Join(dir, "interrupted")
	first, err := pipeline.Run(pipeline.Options{
		WorkDir: intDir, Sources: sources,
		Thresholds: quality.DefaultThresholds(), Workers: 2, StopAfterKey: stopKey,
	})
	if err != nil {
		return "", 0, 0, err
	}
	if !first.Interrupted {
		return "", 0, 0, fmt.Errorf("first run did not interrupt")
	}

	resumed, err := pipeline.Run(pipeline.Options{
		WorkDir: intDir, Sources: sources,
		Thresholds: quality.DefaultThresholds(), Workers: 2,
	})
	if err != nil {
		return "", 0, 0, err
	}
	if resumed.Manifest == nil || len(resumed.Manifest.RunSHA256) != 64 {
		return "", 0, 0, fmt.Errorf("resume produced no valid manifest digest")
	}
	return resumed.Manifest.RunSHA256, resumed.ReusedNodes, resumed.ElapsedMS, nil
}
