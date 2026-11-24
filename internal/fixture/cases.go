package fixture

import "github.com/pion/rtp"

// CaseName enumerates the deterministic demo scenarios.
type CaseName string

const (
	CaseNormal     CaseName = "normal"
	CaseWrapDrift  CaseName = "wrap_drift"
	CaseVFR        CaseName = "vfr"
	CaseTruncated  CaseName = "truncated"
	CaseCrossTrack CaseName = "cross_track"
	CaseRTPLoss    CaseName = "rtp_loss"
)

// AllCases is the fixed demo order used by `--case all`.
var AllCases = []CaseName{
	CaseNormal, CaseWrapDrift, CaseVFR, CaseTruncated, CaseCrossTrack, CaseRTPLoss,
}

// Case bundles the generated container and (where relevant) the RTP stream
// for one scenario, plus the human description of the injected defect.
type Case struct {
	Name        CaseName
	Description string
	MP4         []byte
	Spec        MediaSpec
	RTP         []*rtp.Packet
	RTPSpec     *RTPSpec
	// ExpectUndecidable flags cases whose indexes must not read complete.
	ExpectUndecidable bool
}

// videoTrack is the common 10-second video track: 240 samples at 24 kHz
// clock, 1000-tick delta (~41.7 ms), keyframe every 24 samples (~1 s).
func videoTrack(id uint32) TrackSpec {
	return TrackSpec{
		TrackID: id, Handler: "vide", Timescale: 24000,
		SampleCount: 240, Delta: 1000, SyncEvery: 24,
		Width: 320 << 16, Height: 240 << 16,
	}
}

func audioTrack(id uint32) TrackSpec {
	return TrackSpec{
		TrackID: id, Handler: "soun", Timescale: 48000,
		SampleCount: 480, Delta: 1000,
	}
}

// BuildCase returns one fully synthesized scenario. The seed is reserved
// for future randomized variants; current fixtures are fully deterministic
// and ignore it, which is what makes repeated runs comparable.
func BuildCase(name CaseName, seed int64) Case {
	_ = seed
	switch name {
	case CaseNormal:
		spec := MediaSpec{
			MovieScale: 1000,
			Tracks:     []TrackSpec{videoTrack(1), audioTrack(2)},
		}
		return Case{Name: name, Description: "healthy 10s A/V asset", MP4: BuildMP4(spec), Spec: spec}

	case CaseWrapDrift:
		// Wrap is exercised on the RTP clock (timestamp near 2^32); the
		// container stays valid so timeline monotonicity is verifiable.
		spec := MediaSpec{
			MovieScale: 1000,
			Tracks:     []TrackSpec{videoTrack(1), audioTrack(2)},
		}
		rspec := rtpBase()
		rspec.BaseTimestamp = 0xFFFFFFFF - 10*rspec.TimestampStep
		rspec.Count = 60
		// Remote clock runs 120 ppm fast in addition to the 32-bit wrap.
		rspec.DriftPPM = 120
		return Case{
			Name: name, Description: "32-bit timestamp wrap with clock drift",
			MP4: BuildMP4(spec), Spec: spec,
			RTP: BuildRTP(rspec), RTPSpec: &rspec,
		}

	case CaseVFR:
		v := videoTrack(1)
		// Irregular per-frame deltas summing to the same 10 s media span;
		// the average frame rate must not overwrite these actual gaps.
		v.SttsRuns = []SttsRun{
			{Count: 60, Delta: 500},  // short frames
			{Count: 60, Delta: 1500}, // long frames
			{Count: 60, Delta: 1000}, // nominal frames
			{Count: 60, Delta: 1000},
		}
		v.Delta = 0
		spec := MediaSpec{MovieScale: 1000, Tracks: []TrackSpec{v, audioTrack(2)}}
		return Case{Name: name, Description: "variable frame rate video", MP4: BuildMP4(spec), Spec: spec}

	case CaseTruncated:
		v := videoTrack(1)
		v.TruncatedIndex = true
		spec := MediaSpec{MovieScale: 1000, Tracks: []TrackSpec{v, audioTrack(2)}}
		return Case{
			Name: name, Description: "truncated stts index at track tail",
			MP4: BuildMP4(spec), Spec: spec, ExpectUndecidable: true,
		}

	case CaseCrossTrack:
		v := videoTrack(1)
		a := audioTrack(2)
		v.TruncatedIndex = true
		a.TruncatedIndex = true
		spec := MediaSpec{
			MovieScale: 1000,
			Tracks:     []TrackSpec{v, a},
			Trefs:      []TrefSpec{{FromTrack: 2, RefType: "hint", ToTrackIDs: []uint32{1}}},
		}
		return Case{
			Name: name, Description: "damage spanning both tracks with a tref edge",
			MP4: BuildMP4(spec), Spec: spec, ExpectUndecidable: true,
		}

	case CaseRTPLoss:
		spec := MediaSpec{
			MovieScale: 1000,
			Tracks:     []TrackSpec{videoTrack(1), audioTrack(2)},
		}
		rspec := rtpBase()
		// 20-packet burst with 19 retransmitted => 95% recovery.
		rspec.Bursts = []RTPBurst{{Start: rspec.BaseSeq + 20, Length: 20, Retransmit: 19, DelaySlots: 2}}
		rspec.Duplicates = []uint16{rspec.BaseSeq + 90}
		return Case{
			Name: name, Description: "RTP burst packet loss with retransmission",
			MP4: BuildMP4(spec), Spec: spec,
			RTP: BuildRTP(rspec), RTPSpec: &rspec,
		}
	}
	return Case{Name: name, Description: "unknown case"}
}

func rtpBase() RTPSpec {
	return RTPSpec{
		SSRC: 0x5151, PayloadType: 96, ClockRate: 90000,
		Count: 100, BaseSeq: 1000, BaseTimestamp: 0,
		TimestampStep: 3000,
	}
}
