package rtp

import (
	"fmt"
	"math/big"
	"sort"

	"rtx/internal/model"
)

// Anchor is one explicit correspondence between an RTP timestamp value and a
// media instant. Wrap states how many times the 32-bit RTP timestamp space had
// already rolled over when this anchor was taken, so anchors that straddle a
// wrap stay unambiguous.
type Anchor struct {
	Source       string `json:"source"`
	RTPTimestamp uint32 `json:"rtp_timestamp"`
	Wrap         int64  `json:"wrap"`
	MediaTicks   int64  `json:"media_ticks"`
	ClockRate    int64  `json:"clock_rate"`
}

const rtpSpace = int64(1) << 32

// resolvedAnchor is the validated form: the exact media instant of RTP 0 at
// wrap 0, held as a rational number of media ticks.
type resolvedAnchor struct {
	source    string
	clockRate int64
	// origin is media ticks at extended RTP 0, as an exact rational.
	origin *big.Rat
	value  big.Int // extended RTP of the anchor, for diagnostics
}

// Mapping converts RTP timestamps from several sources into one shared media
// time base using explicit anchors only. It never guesses: a source without an
// anchor, conflicting anchors, or a timestamp that cannot be uniquely
// unwrapped produce a coded error.
type Mapping struct {
	mediaRate model.Rate
	anchors   map[string]resolvedAnchor
	order     []string
}

// NewMapping validates anchors against the unified media rate. Anchors for the
// same source must agree exactly; disagreement is reported rather than merged.
func NewMapping(mediaRate model.Rate, anchors []Anchor) (*Mapping, error) {
	if !mediaRate.Valid() {
		return nil, model.E(model.CodeInvalidInput, "anchor", "invalid media rate %s", mediaRate)
	}
	if len(anchors) == 0 {
		return nil, model.E(model.CodeAnchorAmbiguous, "anchor", "no RTP anchors supplied")
	}
	m := &Mapping{mediaRate: mediaRate.Reduced(), anchors: map[string]resolvedAnchor{}}
	for i, a := range anchors {
		if a.Source == "" {
			return nil, model.E(model.CodeAnchorMismatch, "anchor", "anchor %d has no source", i)
		}
		if a.ClockRate <= 0 {
			return nil, model.E(model.CodeAnchorMismatch, "anchor", "source %s: clock rate %d is not positive", a.Source, a.ClockRate)
		}
		if a.Wrap < 0 {
			return nil, model.E(model.CodeAnchorMismatch, "anchor", "source %s: negative wrap %d", a.Source, a.Wrap)
		}
		ra := resolveAnchor(a, m.mediaRate)
		if prev, ok := m.anchors[a.Source]; ok {
			if prev.clockRate != ra.clockRate {
				return nil, model.E(model.CodeAnchorMismatch, "anchor",
					"source %s: anchors disagree on clock rate %d vs %d", a.Source, prev.clockRate, ra.clockRate)
			}
			if prev.origin.Cmp(ra.origin) != 0 {
				return nil, model.E(model.CodeAnchorMismatch, "anchor",
					"source %s: anchors disagree on media origin (%s vs %s)", a.Source, ratString(prev.origin), ratString(ra.origin))
			}
			continue
		}
		m.anchors[a.Source] = ra
		m.order = append(m.order, a.Source)
	}
	return m, nil
}

func resolveAnchor(a Anchor, mediaRate model.Rate) resolvedAnchor {
	// origin = MediaTicks - extRTP * mediaRate / clockRate, held exactly.
	ext := new(big.Int).SetInt64(a.Wrap)
	ext.Lsh(ext, 32)
	ext.Add(ext, new(big.Int).SetUint64(uint64(a.RTPTimestamp)))
	extScaled := new(big.Rat).SetInt(ext)
	extScaled.Mul(extScaled, big.NewRat(mediaRate.Num, mediaRate.Den))
	extScaled.Quo(extScaled, new(big.Rat).SetInt64(a.ClockRate))
	origin := new(big.Rat).Sub(new(big.Rat).SetInt64(a.MediaTicks), extScaled)
	return resolvedAnchor{source: a.Source, clockRate: a.ClockRate, origin: origin, value: *ext}
}

// MapPacket returns the media instant of p. The packet's source must have an
// anchor and its timestamp must unwrap uniquely relative to that anchor.
func (m *Mapping) MapPacket(p Packet) (model.Time, error) {
	ra, ok := m.anchors[p.Source]
	if !ok {
		return model.Time{}, model.E(model.CodeAnchorAmbiguous, "anchor",
			"source %q has no explicit anchor", p.Source)
	}
	ext, err := unwrapRelative(&ra.value, p.TS)
	if err != nil {
		return model.Time{}, model.E(model.CodeAnchorAmbiguous, "anchor",
			"source %s seq %d timestamp %d: %v", p.Source, p.Seq, p.TS, err)
	}
	ticks, exact := m.rtpTicks(ext, ra)
	if !exact {
		return model.Time{}, model.E(CodeRTPSequence, "anchor",
			"source %s seq %d: timestamp %d does not land on the %s media grid",
			p.Source, p.Seq, p.TS, m.mediaRate)
	}
	return model.Time{Ticks: ticks, Rate: m.mediaRate}, nil
}

// rtpTicks converts an extended RTP value to media ticks through the anchor.
func (m *Mapping) rtpTicks(ext *big.Int, ra resolvedAnchor) (int64, bool) {
	// ticks = origin + ext * mediaRate / clockRate
	term := new(big.Rat).SetInt(ext)
	term.Mul(term, big.NewRat(m.mediaRate.Num, m.mediaRate.Den))
	term.Quo(term, new(big.Rat).SetInt64(ra.clockRate))
	total := new(big.Rat).Add(ra.origin, term)
	if !total.IsInt() {
		return 0, false
	}
	if !total.Num().IsInt64() {
		return 0, false
	}
	return total.Num().Int64(), true
}

// unwrapRelative extends a 32-bit timestamp to the cycle nearest the anchor's
// full extended value. Using the full anchor (not just its low 32 bits) is what
// makes anchors taken after one or more 32-bit rollovers correct. A timestamp
// exactly half the space away is ambiguous and rejected.
func unwrapRelative(anchorExt *big.Int, ts uint32) (*big.Int, error) {
	if anchorExt == nil || anchorExt.Sign() < 0 {
		return nil, fmt.Errorf("invalid anchor value")
	}
	anchorLow := uint32(anchorExt.Uint64())
	delta := int64(int32(ts - anchorLow))
	if delta == -(1 << 31) {
		return nil, fmt.Errorf("timestamp is exactly half the 32-bit space from the anchor")
	}
	// ext = floor(anchor / 2^32) * 2^32 + ts + carry, where carry folds the
	// signed delta's borrow into the cycle count.
	cycle := new(big.Int).Rsh(anchorExt, 32)
	ext := new(big.Int).Lsh(cycle, 32)
	ext.Add(ext, new(big.Int).SetUint64(uint64(anchorLow)))
	ext.Add(ext, big.NewInt(delta))
	if ext.Sign() < 0 {
		return nil, fmt.Errorf("timestamp unwraps before the start of RTP time")
	}
	return ext, nil
}

// Sources lists anchored sources in anchor declaration order.
func (m *Mapping) Sources() []string {
	out := make([]string, len(m.order))
	copy(out, m.order)
	return out
}

// SourceSwitch is a detected change of RTP source along the media timeline.
type SourceSwitch struct {
	From       string
	To         string
	At         model.Time
	PacketIdx  int
	PreviousTS uint32
	CurrentTS  uint32
}

// MapStream maps every packet of a restored stream and returns the switch
// point when the stream's source differs from before. Packets must arrive in
// restored sequence order.
func (m *Mapping) MapStream(s *Stream) ([]model.Time, error) {
	if len(s.Packets) == 0 {
		return nil, nil
	}
	times := make([]model.Time, len(s.Packets))
	for i, p := range s.Packets {
		t, err := m.MapPacket(p)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			c, err := times[i-1].Compare(t)
			if err != nil {
				return nil, err
			}
			if c > 0 {
				return nil, model.E(model.CodeNonMonotonic, "anchor",
					"source %s: packet %d maps to %d after %d", s.Source, i, t.Ticks, times[i-1].Ticks)
			}
		}
		times[i] = t
	}
	return times, nil
}

// DetectSwitches scans restored sources in first-packet media order and records
// every source change. It requires all sources to be resolved.
func (m *Mapping) DetectSwitches(streams []*Stream) ([]SourceSwitch, error) {
	type point struct {
		src string
		at  model.Time
		pkt int
		ts  uint32
	}
	var pts []point
	for _, s := range streams {
		if len(s.Packets) == 0 {
			continue
		}
		at, err := m.MapPacket(s.Packets[0])
		if err != nil {
			return nil, err
		}
		pts = append(pts, point{src: s.Source, at: at, pkt: s.Packets[0].Index, ts: s.Packets[0].TS})
	}
	sort.SliceStable(pts, func(i, j int) bool {
		c, err := pts[i].at.Compare(pts[j].at)
		return err == nil && c < 0
	})
	var out []SourceSwitch
	for i := 1; i < len(pts); i++ {
		if pts[i].src == pts[i-1].src {
			continue
		}
		out = append(out, SourceSwitch{
			From:       pts[i-1].src,
			To:         pts[i].src,
			At:         pts[i].at,
			PacketIdx:  pts[i].pkt,
			PreviousTS: pts[i-1].ts,
			CurrentTS:  pts[i].ts,
		})
	}
	return out, nil
}

func ratString(r *big.Rat) string {
	if r == nil {
		return "<nil>"
	}
	if r.IsInt() {
		return r.Num().String()
	}
	return r.RatString()
}
