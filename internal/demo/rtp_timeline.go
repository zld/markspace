package demo

import (
	"github.com/zld/medcheck/pkg/evidence"
	"github.com/zld/medcheck/pkg/media"
	mrtp "github.com/zld/medcheck/pkg/rtp"
	"github.com/zld/medcheck/pkg/sync"
)

func runRTPAndTimeline(rep *evidence.Report, window int) (*sync.Alignment, error) {
	jb, err := mrtp.NewJitterBuffer("cam0/rtp-video", window)
	if err != nil {
		return nil, err
	}
	order := []uint16{300, 302, 302, 303, 304, 306, 307}
	for n, seq := range order {
		raw, err := mrtp.MakeRawPacket(96, 0x99aa, seq, uint32(seq-300)*3000, seq == 307, []byte("frame-"+itoa(int(seq))))
		if err != nil {
			return nil, err
		}
		if _, err := jb.PushRaw(raw, int64(n)*750, int64(4096+n*188)); err != nil {
			return nil, err
		}
	}
	if _, err := jb.ApplyRepair(mrtp.RepairPacket{
		Sequence: 301, Timestamp: 3000, Payload: []byte("recovered-301"),
		Kind: mrtp.RepairRetransmission, ArrivalUS: 6000, ByteOffset: 5000,
	}); err != nil {
		return nil, err
	}
	out := jb.Flush()
	lateRaw, err := mrtp.MakeRawPacket(96, 0x99aa, 305, 15000, false, []byte("late"))
	if err != nil {
		return nil, err
	}
	if _, err := jb.PushRaw(lateRaw, 20_000, 8192); err != nil {
		return nil, err
	}

	wrapJB, err := mrtp.NewJitterBuffer("cam0/wrap", window)
	if err != nil {
		return nil, err
	}
	for _, seq := range []uint16{65534, 65535, 0, 1} {
		raw, err := mrtp.MakeRawPacket(96, 1, seq, uint32(seq)*3000, false, []byte("w"))
		if err != nil {
			return nil, err
		}
		if _, err := wrapJB.PushRaw(raw, 0, int64(seq)); err != nil {
			return nil, err
		}
	}

	tl := sync.NewTimeline()
	if err := tl.AddClock(sync.TrackClock{TrackID: "cam0/rtp-video", TimeBase: media.Clock90k}); err != nil {
		return nil, err
	}
	for _, p := range out.Packets {
		if _, err := tl.Map("cam0/rtp-video", p.Timestamp, int64(p.ExtSeq), p.ByteOffset); err != nil {
			return nil, err
		}
	}
	if err := tl.AddClock(sync.TrackClock{TrackID: "cam0/video", TimeBase: media.Clock90k}); err != nil {
		return nil, err
	}
	if err := tl.AddClock(sync.TrackClock{
		TrackID: "cam0/audio", TimeBase: media.Rational{Num: 1, Den: 48000}, OffsetUS: 500,
	}); err != nil {
		return nil, err
	}
	var video, audio []sync.Point
	for i := 0; i < 12; i++ {
		v, err := tl.Map("cam0/video", uint32(i*3000), int64(i), int64(i*188))
		if err != nil {
			return nil, err
		}
		video = append(video, v)
		a, err := tl.Map("cam0/audio", uint32(i*1600+i*5), int64(100+i), int64(i*192))
		if err != nil {
			return nil, err
		}
		audio = append(audio, a)
	}
	al, err := sync.Align("cam0/video", "cam0/audio", video, audio)
	if err != nil {
		return nil, err
	}
	// Deliberate timestamp anomaly sample with full error coordinates.
	if _, err := tl.Map("cam0/video", 2990, 999, 9999); err != nil {
		return nil, err
	}

	rep.Recovered = out.Stats.Repaired
	rep.Unrecoverable = out.Stats.Gaps
	rep.Duplicates = out.Stats.Duplicates
	rep.LatePackets = jb.Stats.Late
	rep.SequenceWraps = wrapJB.Stats.Wraps
	rep.AVOffsetUS = al.AVOffsetUS
	rep.DriftUS = al.DriftUS
	rep.MaxAVDiffUS = al.MaxAVDiffUS
	rep.Issues = append(rep.Issues, jb.Issues...)
	rep.Issues = append(rep.Issues, tl.Issues...)

	rep.Check("rtp_recovered_one", out.Stats.Repaired == 1, 1, out.Stats.Repaired, "one in-window repair")
	rep.Check("rtp_unrecoverable_gap_one", out.Stats.Gaps == 1, 1, out.Stats.Gaps, "one missing sequence without repair")
	rep.Check("timeline_drift_within_2000us", al.DriftUS <= 2000 && al.MaxAVDiffUS <= 2000, "<=2000us", al.MaxAVDiffUS, "rational A/V alignment")
	return al, nil
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
