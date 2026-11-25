package fixture

import (
	"fmt"
	"math/rand"

	"archive.example/mediakernel/internal/metadata"
)

// Timeline layout of the generated corpus, in nanoseconds. Every track uses
// its own native timescale; the presentation values below are converted per
// track so the same media instant lands on the same nanosecond.
const (
	// CorpusSpanNS is the media length of a generated archive: 8 seconds.
	CorpusSpanNS int64 = 8_000_000_000
	// SubtitlePeriodNS is the subtitle cadence.
	SubtitlePeriodNS int64 = 500_000_000
	// ChapterPeriodNS is the chapter cadence.
	ChapterPeriodNS int64 = 2_000_000_000
	// ID3PeriodNS is the ID3 timed-metadata cadence.
	ID3PeriodNS int64 = 1_000_000_000
	// SEIPeriodNS is the SEI cadence.
	SEIPeriodNS int64 = 500_000_000
	// EmsgPeriodNS is the emsg cadence.
	EmsgPeriodNS int64 = 1_500_000_000
)

// ticks converts a nanosecond instant into a track's native ticks.
func ticks(ns int64, timescale uint32) uint64 {
	if ns <= 0 {
		return 0
	}
	return uint64(ns * int64(timescale) / 1_000_000_000)
}

// itemsFor builds the timed items of the normal corpus for every kind. The
// boundary kind additionally pushes timestamps to the wrap and range edges.
func itemsFor(kind Kind, rng *rand.Rand) []TimedItem {
	tracks := DefaultTracks()
	var items []TimedItem

	subScale := tracks[2].Timescale // 1000
	chapScale := tracks[3].Timescale
	id3Scale := tracks[4].Timescale
	seiScale := tracks[5].Timescale

	seq := uint16(0)
	for ns := int64(0); ns < CorpusSpanNS; ns += SubtitlePeriodNS {
		seq++
		items = append(items, TimedItem{
			TrackID: 3, BoxType: metadata.BoxSubtitle,
			Presentation: ticks(ns, subScale), Duration: uint32(ticks(SubtitlePeriodNS-100_000_000, subScale)),
			Sequence: seq, Text: subtitleText(rng, seq), Payload: []byte{byte(seq), 0x2A},
		})
	}
	seq = 0
	for ns := int64(0); ns < CorpusSpanNS; ns += ChapterPeriodNS {
		seq++
		items = append(items, TimedItem{
			TrackID: 4, BoxType: metadata.BoxChapter,
			Presentation: ticks(ns, chapScale), Duration: uint32(ticks(ChapterPeriodNS, chapScale)),
			Sequence: seq, Text: fmt.Sprintf("chapter-%02d", seq), Payload: []byte{0xC0, byte(seq)},
		})
	}
	seq = 0
	for ns := int64(0); ns < CorpusSpanNS; ns += ID3PeriodNS {
		seq++
		items = append(items, TimedItem{
			TrackID: 5, BoxType: metadata.BoxID3,
			Presentation: ticks(ns, id3Scale), Duration: uint32(ticks(ID3PeriodNS, id3Scale)),
			Sequence: seq, Text: fmt.Sprintf("id3-%02d", seq),
			Payload: ID3(fmt.Sprintf("title-%02d", seq)),
		})
	}
	seq = 0
	for ns := int64(0); ns < CorpusSpanNS; ns += SEIPeriodNS {
		seq++
		items = append(items, TimedItem{
			TrackID: 6, BoxType: metadata.BoxSEI,
			Presentation: ticks(ns, seiScale), Duration: uint32(ticks(SEIPeriodNS, seiScale)),
			Sequence: seq,
			Payload:  SEI(fmt.Sprintf("sei-%02d", seq), []byte{0xF0, byte(seq)}),
		})
	}
	_ = 0
	if kind == KindBoundary {
		items = append(items, boundarySubtitleItem(rng)...)
	}
	return items
}

// boundarySubtitleItem appends events at zero, one tick, the last representable
// nanosecond of the archive and a timestamp near the 32-bit tick ceiling. The
// latter two are expressed on the subtitle/1kHz track where 32 bits still fit
// inside the 24h limit only for the low part; instead they use the video track
// whose 30kHz scale makes a near-32-bit value exceed the limit, exercising the
// out-of-range path deterministically.
func boundarySubtitleItem(rng *rand.Rand) []TimedItem {
	return []TimedItem{
		{TrackID: 3, BoxType: metadata.BoxSubtitle, Presentation: 0, Duration: 1, Sequence: 900,
			Text: "boundary-zero", Payload: []byte{0}},
		{TrackID: 3, BoxType: metadata.BoxSubtitle, Presentation: 1, Duration: 1, Sequence: 901,
			Text: "boundary-one-tick", Payload: []byte{1}},
		{TrackID: 3, BoxType: metadata.BoxSubtitle, Presentation: uint64(ticks(CorpusSpanNS, 1000)) - 1,
			Duration: 1, Sequence: 902, Text: "boundary-last", Payload: []byte{2}},
		{TrackID: 1, BoxType: metadata.BoxSubtitle, Presentation: 0xFFFF_FFFF, Duration: 1, Sequence: 903,
			Text: "boundary-out-of-range", Payload: []byte{3}},
	}
}

// EmsgTimescale is the independent 90kHz clock emsg uses in the corpus.
const EmsgTimescale uint32 = 90000

// EmsgSpec is one generated emsg box with its own independent timescale.
type EmsgSpec struct {
	Presentation uint64
	Duration     uint32
	Sequence     uint16
	Scheme       string
	Value        string
	Payload      []byte
}

// buildEmsgSpecs lays out emsg events on the 90kHz emsg clock.
func buildEmsgSpecs() []EmsgSpec {
	var out []EmsgSpec
	var seq uint16
	for ns := EmsgPeriodNS; ns < CorpusSpanNS; ns += EmsgPeriodNS {
		seq++
		out = append(out, EmsgSpec{
			Presentation: ticks(ns, EmsgTimescale),
			Duration:     uint32(ticks(1_000_000_000, EmsgTimescale)),
			Sequence:     seq,
			Scheme:       "urn:archive:emsg",
			Value:        fmt.Sprintf("evt-%02d", seq),
			Payload:      []byte{0xE0, byte(seq)},
		})
	}
	return out
}

func subtitleText(rng *rand.Rand, seq uint16) string {
	words := []string{"harbor", "signal", "ledger", "archive", "frame", "cargo"}
	return fmt.Sprintf("%s-%d", words[int(rng.Intn(len(words)))], seq)
}

// groupFragments splits items by wall-clock second and track so that each
// generated moof carries one track's items for a window. Grouping per track
// means a fragment cut by truncation loses at most that track's tail.
func groupFragments(items []TimedItem) []traf {
	var order []winKey
	groups := make(map[winKey][]TimedItem)
	for _, it := range items {
		sec := int64(it.Presentation) / 1000 // coarse; per-track grouping only
		k := winKey{second: sec, track: it.TrackID}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], it)
	}
	// Deterministic window order: by second, then track id.
	sortKeys(order)
	var out []traf
	for _, k := range order {
		out = append(out, traf{trackID: k.track, items: groups[k]})
	}
	return out
}

type winKey struct {
	second int64
	track  uint32
}

func sortKeys(keys []winKey) {
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0; j-- {
			a, b := keys[j-1], keys[j]
			if a.second < b.second || (a.second == b.second && a.track <= b.track) {
				break
			}
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
}
