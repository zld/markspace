package fixture

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"github.com/pion/rtp"

	"continuity/internal/core"
)

// Demo fixture parameters (shared by all cases).
const (
	RefOffset   = int64(5000)       // clock offset in ref ticks
	DriftPPM    = int64(80)         // source-clock drift
	DelayTicks  = int64(3600)       // 40 ms arrival delay
	AnchorC0    = int64(8589600000) // first anchor source tick (multiple of 12500)
	AnchorStep  = int64(12500)      // anchor spacing (exact drift integers)
	PTSBase     = int64(8589720000) // normal/damaged media clock start
	RecoverBase = int64(100000000)  // recover-case media clock start
	SSRC        = uint32(0xC0FFEE)
)

// segPlan describes one manifest entry to generate.
type segPlan struct {
	name     string
	sourceID string
	clock    int64 // source-clock decode time (unwrapped)
	durTicks int64
	durSrc   string // EXTINF text
	dupOf    int    // >=0: byte-identical copy of that plan entry
	truncate bool   // write a truncated moof (damaged case)
}

// Generate writes a deterministic local sample set into dir:
// index.m3u8, index.mpd, segment files, events.rtp, arrivals.tsv.
func Generate(dir, caseName string, seed int64) error {
	var plans []segPlan
	var anchorC0 int64
	anchors := 0
	switch caseName {
	case "normal", "damaged":
		anchorC0 = AnchorC0
		anchors = 100
		for i := 0; i < 5; i++ {
			plans = append(plans, segPlan{
				name: fmt.Sprintf("seg%d.m4s", i), sourceID: srcID(i),
				clock: PTSBase + int64(i)*90000, durTicks: 90000, durSrc: "1.000",
				dupOf: -1,
			})
		}
		dup := segPlan{
			name: "seg2b.m4s", sourceID: "backup",
			clock: PTSBase + 2*90000, durTicks: 90000, durSrc: "1.000", dupOf: 2,
		}
		plans = append([]segPlan{plans[0], plans[1], plans[2], dup}, plans[3], plans[4])
		if caseName == "damaged" {
			plans[2].truncate = true
		}
	case "recover":
		anchorC0 = 99900000
		anchors = 100
		for _, i := range []int{0, 1, 2, 4, 5} { // media-time gap of one segment
			plans = append(plans, segPlan{
				name: fmt.Sprintf("seg%d.m4s", i), sourceID: "primary",
				clock: RecoverBase + int64(i)*180000, durTicks: 180000, durSrc: "2.000",
				dupOf: -1,
			})
		}
	default:
		return fmt.Errorf("unknown case %q", caseName)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	rnd := rand.New(rand.NewSource(seed))
	built := make([][]byte, len(plans))
	for i, p := range plans {
		var data []byte
		var err error
		switch {
		case p.truncate:
			data, err = truncatedSegment()
		case p.dupOf >= 0:
			data = built[p.dupOf]
		default:
			data, err = buildCaseSegment(p, rnd)
		}
		if err != nil {
			return err
		}
		built[i] = data
		if err := os.WriteFile(filepath.Join(dir, p.name), data, 0o644); err != nil {
			return err
		}
	}
	if err := writeHLS(dir, plans); err != nil {
		return err
	}
	if err := writeMPD(dir, plans); err != nil {
		return err
	}
	if err := writeEvents(dir, anchorC0, anchors, rnd); err != nil {
		return err
	}
	return writeArrivals(dir, plans, anchorC0, anchors)
}

// srcID assigns origin identities around the primary/backup switch.
func srcID(i int) string {
	if i >= 3 {
		return "backup"
	}
	return "primary"
}

// buildCaseSegment builds one VFR fMP4 segment (60 samples, sum=durTicks).
func buildCaseSegment(p segPlan, rnd *rand.Rand) ([]byte, error) {
	n := 60
	durs := make([]uint32, n)
	base := p.durTicks / int64(n)
	var sum int64
	for j := 0; j < n; j++ {
		durs[j] = uint32(base + int64(j%5)*3 - 6) // VFR: +/-6 ticks around base
		sum += int64(durs[j])
	}
	durs[n-1] = uint32(int64(durs[n-1]) + (p.durTicks - sum)) // exact total
	payload := make([]byte, 200+int(rnd.Int31n(200)))
	if _, err := rnd.Read(payload); err != nil {
		return nil, err
	}
	return BuildSegment(SegmentSpec{
		TrackID:   1,
		BaseTime:  uint64(p.clock % (1 << 33)),
		Durations: durs,
		Payload:   payload,
	})
}

// truncatedSegment returns ftyp (24 bytes) + a moof header declaring 200
// bytes + 4 payload bytes, so the moof at byte 24 is truncated.
func truncatedSegment() ([]byte, error) {
	full, err := BuildSegment(SegmentSpec{TrackID: 1, Durations: []uint32{1500}, Payload: []byte("x")})
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), full[:24]...)
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:4], 200)
	copy(hdr[4:8], "moof")
	out = append(out, hdr...)
	return append(out, 0, 0, 0, 0), nil
}

func writeHLS(dir string, plans []segPlan) error {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
	for _, p := range plans {
		fmt.Fprintf(&b, "#EXTINF:%s,\n%s\n", p.durSrc, p.name)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte(b.String()), 0o644)
}

func writeMPD(dir string, plans []segPlan) error {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\"?>\n<MPD type=\"static\">\n  <Period>\n    <AdaptationSet>\n      <Representation id=\"v1\">\n")
	b.WriteString("        <SegmentList timescale=\"90000\" startNumber=\"0\">\n")
	for _, p := range plans {
		fmt.Fprintf(&b, "          <SegmentURL media=\"%s\"/>\n", p.name)
	}
	b.WriteString("        </SegmentList>\n      </Representation>\n    </AdaptationSet>\n  </Period>\n</MPD>\n")
	return os.WriteFile(filepath.Join(dir, "index.mpd"), []byte(b.String()), 0o644)
}

// writeEvents emits RTP anchor records: source clock C, arrival at
// ref(C) = RefOffset + C*(1+80ppm), exact at multiples of 12500.
func writeEvents(dir string, c0 int64, n int, rnd *rand.Rand) error {
	var out []byte
	for k := 0; k < n; k++ {
		c := c0 + int64(k)*AnchorStep
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version: 2, SequenceNumber: uint16(k),
				Timestamp: uint32(c % (1 << 32)), SSRC: SSRC,
			},
			Payload: []byte{byte(k)},
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return err
		}
		var hdr [10]byte
		binary.BigEndian.PutUint64(hdr[0:8], uint64(RefOffset+c+c*DriftPPM/1000000))
		binary.BigEndian.PutUint16(hdr[8:10], uint16(len(raw)))
		out = append(out, hdr[:]...)
		out = append(out, raw...)
	}
	return os.WriteFile(filepath.Join(dir, "events.rtp"), out, 0o644)
}

// writeArrivals maps each entry's media time through the kernel's own
// clock model (core.UnwrapRecords + fitted clock) plus the 40 ms pipeline
// delay, so delays and deltas check out exactly downstream.
func writeArrivals(dir string, plans []segPlan, anchorC0 int64, anchors int) error {
	var events []core.Event
	for k := 0; k < anchors; k++ {
		c := anchorC0 + int64(k)*AnchorStep
		events = append(events, core.Event{
			ArrivalRef: RefOffset + c + c*DriftPPM/1000000,
			RTPTime:    uint32(c % (1 << 32)),
			Seq:        uint16(k),
		})
	}
	var records []core.Record
	for i, p := range plans {
		records = append(records, core.Record{
			Index: int64(i),
			Info:  &core.SegmentInfo{BaseTime: p.clock % (1 << 33)},
		})
	}
	unwrapped, clk, _, err := core.UnwrapRecords(events, records)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# uri\tsource_id\tarrival_ref\n")
	for i, p := range plans {
		ref, ok := clk.ToRef(unwrapped[i])
		if !ok {
			return fmt.Errorf("no clock fit for %s", p.name)
		}
		arrival := ref + DelayTicks
		if p.dupOf >= 0 {
			arrival += 450 // duplicate copy arrives 5 ms later
		}
		fmt.Fprintf(&b, "%s\t%s\t%d\n", p.name, p.sourceID, arrival)
	}
	return os.WriteFile(filepath.Join(dir, "arrivals.tsv"), []byte(b.String()), 0o644)
}
