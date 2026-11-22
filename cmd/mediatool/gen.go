package main

import (
	"crypto/sha256"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediapipe/internal/audio"
	"mediapipe/internal/ingest"
)

const (
	trackSeconds  = 30
	segSeconds    = 2
	segsPerTrack  = trackSeconds / segSeconds
	rtpPacketsSec = 50 // 20 ms packets
	rtpWindow     = 8
)

// TrackSample describes one generated track on disk.
type TrackSample struct {
	ID        string
	Format    string // "hls" | "dash"
	InRate    int
	InLayout  audio.Layout
	Manifest  string // absolute path to playlist/MPD
	Checksums map[string][32]byte
}

// SampleSet is a fully generated local test corpus.
type SampleSet struct {
	Dir         string
	Tracks      []*TrackSample
	RTP         map[string][]*rtp.Packet // track ID -> packets in received order
	CorruptOnce map[string]bool          // URIs whose first read is corrupted
}

// Cleanup removes the generated tree.
func (s *SampleSet) Cleanup() { os.RemoveAll(s.Dir) }

// Generate builds a deterministic local corpus. Fault classes when enabled:
// manifest gap, truncated segment, checksum corruption, RTP reorder+dup,
// RTP loss beyond the recovery window.
func Generate(seed int64, nTracks int, faults string) (*SampleSet, error) {
	dir, err := os.MkdirTemp("", "mediatool-samples-*")
	if err != nil {
		return nil, err
	}
	ss := &SampleSet{
		Dir:         dir,
		RTP:         map[string][]*rtp.Packet{},
		CorruptOnce: map[string]bool{},
	}
	withFaults := faults == "all"
	for i := 0; i < nTracks; i++ {
		id := fmt.Sprintf("track%d", i)
		ts := &TrackSample{
			ID:        id,
			InRate:    48000,
			InLayout:  audio.Stereo,
			Checksums: map[string][32]byte{},
		}
		if i%2 == 1 {
			ts.InRate = 44100
			ts.InLayout = audio.Mono
		}
		if i%2 == 0 {
			ts.Format = "hls"
		} else {
			ts.Format = "dash"
		}
		rng := rand.New(rand.NewSource(seed*7919 + int64(i)*104729))

		fault := -1
		if withFaults {
			fault = i % 4 // 0 truncate, 1 manifest gap, 2 checksum, 3 rtp loss
		}
		segDir := filepath.Join(dir, id)
		if err := os.MkdirAll(segDir, 0o755); err != nil {
			return nil, err
		}
		frames := segSeconds * ts.InRate
		dropped := -1
		if fault == 1 {
			dropped = 7 // manifest gap: segment omitted from the playlist
		}
		for s := 0; s < segsPerTrack; s++ {
			uri := fmt.Sprintf("%s/seg%d.m4s", id, s)
			payload := synthPCM(rng, frames, ts.InLayout, 220*float64(i+1))
			ts.Checksums[uri] = sha256.Sum256(payload)
			data := ingest.BuildSegment(payload)
			if fault == 0 && s == 3 {
				data = data[:len(data)-1000] // truncated segment
			}
			if fault == 2 && s == 5 {
				ss.CorruptOnce[uri] = true // transient checksum corruption
			}
			if err := os.WriteFile(filepath.Join(segDir, fmt.Sprintf("seg%d.m4s", s)), data, 0o644); err != nil {
				return nil, err
			}
		}
		manifest, err := writeManifest(dir, ts, dropped)
		if err != nil {
			return nil, err
		}
		ts.Manifest = manifest
		ss.Tracks = append(ss.Tracks, ts)
		ss.RTP[id] = genRTP(rng, i, fault == 3)
	}
	return ss, nil
}

// synthPCM renders a deterministic sine as interleaved s16le.
func synthPCM(rng *rand.Rand, frames int, layout audio.Layout, freq float64) []byte {
	ch := layout.Channels()
	out := make([]byte, frames*ch*2)
	phase := rng.Float64() // per-segment phase jitter, deterministic per seed
	for f := 0; f < frames; f++ {
		for c := 0; c < ch; c++ {
			v := 0.05 * math.Sin(2*math.Pi*freq*float64(f)/48000+phase+float64(c)*0.1)
			s := int16(v * 32767)
			out[(f*ch+c)*2] = byte(s)
			out[(f*ch+c)*2+1] = byte(s >> 8)
		}
	}
	return out
}
