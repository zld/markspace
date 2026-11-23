package index

import (
	"io"

	"mediatool/internal/clock"
	"mediatool/internal/fault"
	"mediatool/internal/rtp"
)

// TrackInfo re-exports the scanner's track identity for callers that build a
// scan config without importing the rtp package directly.
type TrackInfo = rtp.TrackInfo

// ScanConfig describes the tracks present in a capture.
type ScanConfig struct {
	// Tracks lists the track identity and RTP clock rate per SSRC.
	Tracks map[uint32]TrackInfo
	// Kinds maps track ID to "audio"/"video".
	Kinds map[uint32]string
	// Window is the scanner reorder window.
	Window int
	// IsKeyframe classifies a packet as a random-access point.
	IsKeyframe func(*rtp.Event) bool
	// Digest, when non-empty, is the SHA-256 of the entire input. Streaming
	// callers compute it in a separate pass; if empty it is computed here.
	Digest string
	// InputBytes, when positive, is the total input length.
	InputBytes int64
}

// ScanResult holds the built index plus the timeline and scanner totals.
type ScanResult struct {
	Index    *Index
	Timeline *clock.Timeline
	Stats    rtp.Stats
	// Faults is every located fault seen during the scan, in input order.
	Faults []*fault.Fault
}

// IsVideoKeyframe reads the fixture's payload sentinel: the first payload byte
// is 1 for a random-access frame.
func IsVideoKeyframe(ev *rtp.Event) bool {
	return len(ev.Payload) > 0 && ev.Payload[0] == 1
}

// keyframeClassifier wraps the caller's classifier so audio packets, which are
// independently decodable, count as anchors without codec inspection.
func keyframeClassifier(base func(*rtp.Event) bool, kinds map[uint32]string) func(*rtp.Event) bool {
	if base == nil {
		return nil
	}
	return func(ev *rtp.Event) bool {
		if kinds[ev.TrackID] == "audio" {
			return true
		}
		return base(ev)
	}
}

// ScanReader scans a capture from a stream. Memory is bounded by the scanner
// window plus retained keyframe records; packet payloads are never retained.
func ScanReader(r io.Reader, cfg ScanConfig) (*ScanResult, error) {
	sc, err := rtp.NewScanner(r, rtp.Config{
		Tracks:     cfg.Tracks,
		Window:     cfg.Window,
		IsKeyframe: keyframeClassifier(cfg.IsKeyframe, cfg.Kinds),
	})
	if err != nil {
		return nil, err
	}
	tl := clock.New()
	b := NewBuilder(cfg.Digest, Config{Window: cfg.Window, TrackKind: cfg.Kinds})
	for ssrc, info := range cfg.Tracks {
		tl.Register(info.TrackID, ssrc, info.ClockRate)
		b.SetClockRate(info.TrackID, info.ClockRate)
	}
	b.SetInputBytes(cfg.InputBytes)

	var faults []*fault.Fault
	for {
		ev, ok, err := sc.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		e := ev
		var stamp clock.Stamped
		switch e.Kind {
		case rtp.EventPacket:
			if c, ok := tl.Clock(e.TrackID); ok && e.TrackID != 0 {
				stamp = c.Map(e.Timestamp)
				// A packet with no trusted clock is still indexed so its damage
				// stays visible, but it is flagged rather than timed at zero.
				if !stamp.Valid {
					e.Faults = append(e.Faults, (&fault.Fault{Kind: fault.KindRTCPInvalid,
						TrackID: e.TrackID, Offset: e.Offset, Recovery: fault.RecoverContinue,
						Severity: fault.SeverityDamaged}).WithDetail("%s", stamp.Reason))
				}
			}
		case rtp.EventSenderReport:
			if e.SR != nil {
				if err := tl.ObserveSR(*e.SR); err != nil {
					if f, ok := fault.As(err); ok {
						e.Faults = append(e.Faults, f)
					}
				}
			}
		}
		b.AddEvent(&e, stamp)
		if stamp.Valid && e.TrackID != 0 {
			if c, ok := tl.Clock(e.TrackID); ok {
				if tr := b.Track(e.TrackID); tr.BaseNTPNs == 0 {
					tr.BaseNTPNs = uint64(c.BaseNTPNs())
				}
			}
		}
		faults = append(faults, e.Faults...)
	}
	stats := sc.Stats()
	b.SetStats(stats)
	b.SetUntrusted(tl.Untrusted())
	ix := b.Build()
	for i := range ix.Tracks {
		var u []clock.Untrusted
		for _, all := range tl.Untrusted() {
			if all.TrackID == ix.Tracks[i].TrackID {
				u = append(u, all)
			}
		}
		ix.Tracks[i].Untrusted = u
	}
	return &ScanResult{Index: ix, Timeline: tl, Stats: stats, Faults: faults}, nil
}

// TrustPredicate returns a function reporting whether a media time is outside
// every untrusted interval for the given track.
func (ix *Index) TrustPredicate(trackID uint32) func(mediaMs int64) bool {
	tr, ok := ix.Track(trackID)
	if !ok {
		return func(int64) bool { return true }
	}
	var baseNs int64
	if tr.Time.Valid {
		baseNs = int64(tr.BaseNTPNs)
	}
	return func(mediaMs int64) bool {
		ns := baseNs + mediaMs*1e6
		for _, u := range tr.Untrusted {
			if ns >= u.FromNs && ns <= u.ToNs {
				return false
			}
		}
		return true
	}
}
