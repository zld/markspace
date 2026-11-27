// Package fixture generates every sample the demo and the acceptance tests
// use.  Nothing is read from disk or the network: transport streams, legacy
// manifests and poster frames are all synthesised deterministically from the
// release name and asset name, so runs are byte-for-byte reproducible.
package fixture

import (
	"crypto/sha256"
	"encoding/binary"
	"math"

	"example.com/assetpack/internal/ts"
)

// Audio sample description of a generated asset.
type AssetSpec struct {
	Name      string
	Rate      int
	Channels  int
	Frames    int
	Freq      float64
	Amplitude float64
}

// FramesFor returns a deterministic frame count in [600, 900] ms.  The
// minimum keeps every asset long enough for BS.1770 gated loudness, which
// needs at least one 400 ms measurement block.
func FramesFor(spec AssetSpec) int {
	if spec.Frames > 0 {
		return spec.Frames
	}
	h := sha256.Sum256([]byte(spec.Name))
	ms := 600 + int(h[0])%300
	return spec.Rate * ms / 1000
}

// PCM renders deterministic 16-bit little-endian interleaved PCM.
func PCM(spec AssetSpec) []byte {
	frames := FramesFor(spec)
	out := make([]byte, frames*spec.Channels*2)
	amp := spec.Amplitude
	if amp == 0 {
		amp = 0.3
	}
	freq := spec.Freq
	if freq == 0 {
		freq = 440
	}
	pos := 0
	for i := 0; i < frames; i++ {
		// A slow amplitude envelope keeps gating honest.
		env := 0.6 + 0.4*math.Sin(2*math.Pi*1.5*float64(i)/float64(spec.Rate))
		for c := 0; c < spec.Channels; c++ {
			phase := float64(i)*freq/float64(spec.Rate) + float64(c)*0.25
			v := amp * env * math.Sin(2*math.Pi*phase)
			s := int16(clamp(v) * 32767)
			binary.LittleEndian.PutUint16(out[pos:], uint16(s))
			pos += 2
		}
	}
	return out
}

func clamp(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}

// PSIVersion selects which PAT/PMT version a generated stream uses.
type PSIVersion struct {
	PAT uint8
	PMT uint8
	// AudioPID carries the elementary audio.
	AudioPID uint16
}

// StreamOptions controls the generated transport stream.
type StreamOptions struct {
	Asset    AssetSpec
	PID      uint16
	PCRPID   uint16
	PMTPID   uint16
	Program  uint16
	PSI      PSIVersion
	PCRStart uint64
	WrapPCR  bool
	// PESBytes is the elementary payload per PES packet.
	PESBytes int
}

func defaultStreamOptions(o StreamOptions) StreamOptions {
	if o.PID == 0 {
		o.PID = 0x102
	}
	if o.PCRPID == 0 {
		o.PCRPID = 0x101
	}
	if o.PMTPID == 0 {
		o.PMTPID = 0x1000
	}
	if o.Program == 0 {
		o.Program = 1
	}
	if o.PSI.AudioPID == 0 {
		o.PSI.AudioPID = o.PID
	}
	if o.PESBytes <= 0 {
		o.PESBytes = 4096
	}
	if o.PCRStart == 0 {
		o.PCRStart = 900000
	}
	return o
}

// BuildTS synthesises a transport stream with PAT, PMT, PCR and PES audio.
func BuildTS(opts StreamOptions) []byte {
	o := defaultStreamOptions(opts)
	rate := o.Asset.Rate
	if rate == 0 {
		rate = 48000
	}
	channels := o.Asset.Channels
	if channels == 0 {
		channels = 2
	}
	pcm := PCM(AssetSpec{Name: o.Asset.Name, Rate: rate, Channels: channels,
		Frames: o.Asset.Frames, Freq: o.Asset.Freq, Amplitude: o.Asset.Amplitude})
	frameBytes := channels * 2
	totalFrames := len(pcm) / frameBytes

	var out []byte
	cc := map[uint16]uint8{}
	next := func(pid uint16) uint8 {
		v := cc[pid]
		cc[pid] = (v + 1) & 0x0F
		return v
	}

	pat := buildPAT(o.PSI.PAT, o.Program, o.PMTPID)
	pmt := buildPMT(o.PSI.PMT, o.Program, o.PCRPID, o.PID, channels, rate)
	out = append(out, packetizePSI(ts.PIDPAT, next(ts.PIDPAT), pat)...)
	out = append(out, packetizePSI(o.PMTPID, next(o.PMTPID), pmt)...)

	// 20 ms PES packets.
	framesPerPES := rate / 50
	if framesPerPES < 1 {
		framesPerPES = 1
	}
	pcrBase := o.PCRStart
	if o.WrapPCR {
		// Start just short of the 33-bit modulus so the generated stream
		// crosses it while staying continuous.
		pcrBase = (1 << 33) - uint64(ts.PCRClockHz)/2
	}
	offset := 0
	for pos := 0; pos < totalFrames; pos += framesPerPES {
		end := pos + framesPerPES
		if end > totalFrames {
			end = totalFrames
		}
		payload := pcm[pos*frameBytes : end*frameBytes]
		pts := int64(pcrBase) + int64(pos)*int64(ts.PCRClockHz)/int64(rate)
		if o.WrapPCR {
			pts = int64(uint64(pcrBase)+uint64(pos)*uint64(ts.PCRClockHz)/uint64(rate)) & 0x1FFFFFFFF
		}
		pes := buildPES(pts, payload)
		frag := o.PESBytes
		if frag > 184 {
			frag = 184
		}
		for i := 0; i < len(pes); i += frag {
			chunkEnd := i + frag
			if chunkEnd > len(pes) {
				chunkEnd = len(pes)
			}
			rawPCR := pcrBase + uint64(pos)*uint64(ts.PCRClockHz)/uint64(rate)
			if o.WrapPCR {
				rawPCR &= (1 << 33) - 1
			}
			out = append(out, buildAVPacket(o.PCRPID, next(o.PCRPID), rawPCR, i == 0)...)
			out = append(out, packetizePES(o.PID, next(o.PID), i == 0, pes[i:chunkEnd])...)
			offset++
		}
	}
	return out
}
