// Package demo implements the mediapack demo command: deterministic input
// generation, the four-stage pipeline with crash/resume, and the fixture
// verification suite.
package demo

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"os"

	"github.com/pion/rtp"

	"mediapack/media"
	"mediapack/safefs"
)

// Params are the digestible run parameters.
type Params struct {
	Seed         int64 `json:"seed"`
	FPS          int64 `json:"fps"`
	Width        int   `json:"width"`
	Height       int   `json:"height"`
	DurationSecs int64 `json:"duration_secs"`
	TargetBps    int64 `json:"target_bps"`
}

// InputSet locates the generated input assets.
type InputSet struct {
	VideoMP4 string `json:"video_mp4"`
	AudioMP4 string `json:"audio_mp4"`
	RTPLog   string `json:"rtp_log"`
}

const (
	demoFrames      = 120
	demoVideoBytes  = 396000 // sums to exactly 792000 bps over 4s
	rtpSeqBase      = 40000
	rtpTSBase       = 2_000_000_000 // non-zero RTP timestamp origin
	rtpClockRate    = 90000
	rtpReorderWin   = 8
	rtpGapThreshold = 3
	audioSamples    = 192
	audioSampleSize = 128
)

func fillFunc(seed int64) func(track uint32, index int, size int) []byte {
	return func(track uint32, index int, size int) []byte {
		x := uint64(seed)*0x9E3779B97F4A7C15 ^ uint64(track)<<32 ^ uint64(index) | 1
		b := make([]byte, size)
		for i := 0; i+8 <= size; i += 8 {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			binary.LittleEndian.PutUint64(b[i:], x)
		}
		return b
	}
}

// GenerateInputs deterministically creates the demo assets inside outDir.
func GenerateInputs(outDir string, seed int64) (InputSet, error) {
	if _, err := safefs.MkdirAll(outDir, "inputs", 0o755); err != nil {
		return InputSet{}, err
	}
	rng := rand.New(rand.NewSource(seed))
	var in InputSet
	// video: 120 samples whose sizes sum to exactly demoVideoBytes
	sizes := make([]uint32, demoFrames)
	sum := 0
	for i := 0; i < demoFrames-1; i++ {
		sizes[i] = uint32(3300 + rng.Intn(21) - 10)
		sum += int(sizes[i])
	}
	sizes[demoFrames-1] = uint32(demoVideoBytes - sum)
	in.VideoMP4, _ = safefs.Join(outDir, "inputs/video.mp4")
	err := media.WriteSyntheticMP4(in.VideoMP4, []media.SynthTrack{
		{TrackID: 1, MediaType: "vide", Timescale: 90000, Delta: 3000, Sizes: sizes, Fill: fillFunc(seed)},
	})
	if err != nil {
		return in, err
	}
	// audio: 192 samples of 1024 ticks @48kHz
	asizes := make([]uint32, audioSamples)
	for i := range asizes {
		asizes[i] = audioSampleSize
	}
	in.AudioMP4, _ = safefs.Join(outDir, "inputs/audio.mp4")
	err = media.WriteSyntheticMP4(in.AudioMP4, []media.SynthTrack{
		{TrackID: 2, MediaType: "soun", Timescale: 48000, Delta: 1024, Sizes: asizes, Fill: fillFunc(seed + 1)},
	})
	if err != nil {
		return in, err
	}
	// rtp log with a loss burst, duplicates and in-window reordering
	in.RTPLog, _ = safefs.Join(outDir, "inputs/rtp.log")
	if err := media.WriteRTPLog(in.RTPLog, demoRTPPackets(seed)); err != nil {
		return in, err
	}
	return in, nil
}

func demoRTPPackets(seed int64) []*rtp.Packet {
	drop := map[int]bool{50: true, 51: true, 52: true, 53: true}
	dup := map[int]bool{10: true, 61: true}
	var order []int
	for i := 0; i < demoFrames; i++ {
		if !drop[i] {
			order = append(order, i)
		}
	}
	for _, p := range [][2]int{{20, 21}, {70, 71}} { // adjacent swaps inside the reorder window
		ia, ib := -1, -1
		for k, v := range order {
			if v == p[0] {
				ia = k
			}
			if v == p[1] {
				ib = k
			}
		}
		if ia >= 0 && ib >= 0 {
			order[ia], order[ib] = order[ib], order[ia]
		}
	}
	mk := func(i int) *rtp.Packet {
		return &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96,
				SequenceNumber: uint16(rtpSeqBase + i),
				Timestamp:      uint32(rtpTSBase + i*3000),
				SSRC:           0x5eed,
			},
			Payload: fillFunc(seed+2)(9, i, 200),
		}
	}
	var pkts []*rtp.Packet
	for _, i := range order {
		pkts = append(pkts, mk(i))
		if dup[i] {
			pkts = append(pkts, mk(i)) // duplicate must not be emitted twice
		}
	}
	return pkts
}

// DigestInputs hashes the input assets plus the timeline definition.
func DigestInputs(in InputSet, extra any) (string, error) {
	h := sha256.New()
	for _, p := range []string{in.VideoMP4, in.AudioMP4, in.RTPLog} {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		h.Write(b)
	}
	b, err := json.Marshal(extra)
	if err != nil {
		return "", err
	}
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DigestParams hashes the run parameters.
func DigestParams(p Params) string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
