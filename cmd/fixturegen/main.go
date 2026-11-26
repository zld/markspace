package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediasync/internal/ingest"
)

const payloadSize = 24

func main() {
	dir := flag.String("dir", ".tmp/fixtures", "fixture output root")
	seed := flag.Int64("seed", 7, "deterministic generator seed")
	flag.Parse()

	scenarios := []struct {
		name string
		fn   func(string, int64) error
	}{
		{"valid", writeValid},
		{"changed", writeChanged},
		{"corrupt-rtp", writeCorruptRTP},
		{"corrupt-mp4", writeCorruptMP4},
		{"overflow", writeOverflow},
		{"quality-drift", writeQualityDrift},
	}
	for _, sc := range scenarios {
		path := filepath.Join(*dir, sc.name)
		if err := sc.fn(path, *seed); err != nil {
			fmt.Fprintf(os.Stderr, "fixturegen %s: %v\n", sc.name, err)
			os.Exit(1)
		}
		fmt.Printf("generated=%s\n", path)
	}
}

func writeValid(dir string, seed int64) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	rng := rand.New(rand.NewSource(seed))
	if err := os.WriteFile(filepath.Join(dir, "opus.rtpbin"), validRTP(rng), 0o644); err != nil {
		return err
	}
	sizes := []uint32{24, 28, 26, 32, 30, 27}
	if err := os.WriteFile(filepath.Join(dir, "aac.mp4"), buildAACMP4(sizes), 0o644); err != nil {
		return err
	}
	subs := validSubtitles(false)
	if err := writeJSON(filepath.Join(dir, "subtitles.json"), subs); err != nil {
		return err
	}
	manifest := ingest.Manifest{
		Schema: "mediasync-fixture/v1", Seed: seed,
		Tracks: []ingest.TrackSpec{
			{ID: "opus-main", Kind: ingest.KindOpus, Path: "opus.rtpbin", TimeScale: 48000, ReorderWindow: 4},
			{ID: "aac-main", Kind: ingest.KindAAC, Path: "aac.mp4", TimeScale: 44100},
			{ID: "subtitles-main", Kind: ingest.KindSubtitle, Path: "subtitles.json", TimeScale: 1000},
		},
		FrameTimesMS: []int64{0, 1000, 2000, 3000},
	}
	return writeJSON(filepath.Join(dir, ingest.ManifestName), manifest)
}

func writeChanged(dir string, seed int64) error {
	if err := writeValid(dir, seed); err != nil {
		return err
	}
	subs := validSubtitles(true)
	return writeJSON(filepath.Join(dir, "subtitles.json"), subs)
}

func writeCorruptRTP(dir string, seed int64) error {
	if err := writeValid(dir, seed); err != nil {
		return err
	}
	records := make([][]byte, 0, 4)
	for i, seq := range []uint16{2000, 2001, 2002, 2003} {
		rec := mustRTP(seq, uint32(i*960), seed)
		if i == 3 {
			rec[0] &= 0x3f // RTP version 0: illegal for this pipeline
		}
		records = append(records, rec)
	}
	return os.WriteFile(filepath.Join(dir, "opus.rtpbin"), container(records...), 0o644)
}

func writeCorruptMP4(dir string, seed int64) error {
	if err := writeValid(dir, seed); err != nil {
		return err
	}
	path := filepath.Join(dir, "aac.mp4")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data[:len(data)-18], 0o644)
}

func writeOverflow(dir string, seed int64) error {
	if err := writeValid(dir, seed); err != nil {
		return err
	}
	path := filepath.Join(dir, ingest.ManifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m ingest.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	for i := range m.Tracks {
		if m.Tracks[i].Kind == ingest.KindOpus {
			m.Tracks[i].OriginTicks = math.MaxInt64 - 1024
		}
	}
	return writeJSON(path, m)
}

func writeQualityDrift(dir string, seed int64) error {
	if err := writeValid(dir, seed); err != nil {
		return err
	}
	subs := validSubtitles(false)
	drift := subs.Cues[0].StartMS + 5
	subs.Cues[0].ReferenceStartMS = &drift
	return writeJSON(filepath.Join(dir, "subtitles.json"), subs)
}

func validSubtitles(changed bool) ingest.SubtitleFile {
	ref0, ref2000 := int64(0), int64(2000)
	first := "你好，世界 — Opus/AAC 同步复核"
	if changed {
		first += " [changed]"
	}
	return ingest.SubtitleFile{Cues: []ingest.SubtitleCue{
		{Index: 1, StartMS: 0, EndMS: 1400, ReferenceStartMS: &ref0, Text: first},
		{Index: 2, StartMS: 2000, EndMS: 3400, ReferenceStartMS: &ref2000, Text: "字幕错位时间段检查 · multilingual dialogue"},
	}}
}

func validRTP(rng *rand.Rand) []byte {
	// Ten datagrams: nine unique sequences 1000..1009, duplicate 1002,
	// missing 1005, and 1004 before 1003.
	order := []uint16{1000, 1001, 1002, 1002, 1004, 1003, 1006, 1007, 1008, 1009}
	records := make([][]byte, 0, len(order))
	for _, seq := range order {
		ts := uint32(uint32(seq-1000) * 960)
		payload := make([]byte, payloadSize)
		_, _ = rng.Read(payload)
		rec, err := (&rtp.Packet{Header: rtp.Header{
			Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 0x07070707,
		}, Payload: payload}).Marshal()
		if err != nil {
			panic(err)
		}
		records = append(records, rec)
	}
	return container(records...)
}

func mustRTP(seq uint16, ts uint32, seed int64) []byte {
	payload := make([]byte, payloadSize)
	rng := rand.New(rand.NewSource(seed + int64(seq)))
	_, _ = rng.Read(payload)
	rec, err := (&rtp.Packet{Header: rtp.Header{
		Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 9,
	}, Payload: payload}).Marshal()
	if err != nil {
		panic(err)
	}
	return rec
}

func container(records ...[]byte) []byte {
	out := make([]byte, 0)
	for _, rec := range records {
		var prefix [2]byte
		binary.BigEndian.PutUint16(prefix[:], uint16(len(rec)))
		out = append(out, prefix[:]...)
		out = append(out, rec...)
	}
	return out
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
