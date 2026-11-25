// Package fixture generates deterministic normal, corrupted and edge-case
// samples from a seed. No external assets are used; MP4 files are written
// through the remux writer so demux and remux stay symmetric.
package fixture

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediacore/internal/model"
	"mediacore/internal/remux"
)

// Sample describes one generated input file.
type Sample struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Kind     string `json:"kind"` // "mp4" or "rtp"
	ExpectOK bool   `json:"expect_ok"`
	Note     string `json:"note,omitempty"`
}

// OutputTracks is the fixed output layout every generated MP4 uses.
func OutputTracks() []model.OutputTrack {
	return []model.OutputTrack{
		{ID: "out:v", Kind: model.TrackVideo, Timescale: 90000,
			Accepts: []model.EventKind{model.EventMedia, model.EventSEI}},
		{ID: "out:s", Kind: model.TrackSubtitle, Timescale: 1000,
			Accepts: []model.EventKind{model.EventSubtitle}},
		{ID: "out:m", Kind: model.TrackMeta, Timescale: 90000,
			Accepts: []model.EventKind{model.EventID3, model.EventEMSG, model.EventChapter}},
	}
}

func randBytes(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	r.Read(b)
	return b
}

// avccSample builds a deterministic AVCC sample; withSEI prepends an SEI NAL.
func avccSample(r *rand.Rand, withSEI bool) []byte {
	var out []byte
	if withSEI {
		sei := append([]byte{0x06, 0x05}, randBytes(r, 6)...)
		var lb [4]byte
		binary.BigEndian.PutUint32(lb[:], uint32(len(sei)))
		out = append(out, lb[:]...)
		out = append(out, sei...)
	}
	nal := append([]byte{0x65}, randBytes(r, 24)...)
	var lb [4]byte
	binary.BigEndian.PutUint32(lb[:], uint32(len(nal)))
	out = append(out, lb[:]...)
	return append(out, nal...)
}

// mediaEvents builds the shared event set; subShift shifts subtitle ticks
// (drift sample), vfr selects irregular video durations.
func mediaEvents(r *rand.Rand, subShift int64, vfr bool) []model.Event {
	var events []model.Event
	durs := []int64{3000, 3000, 3000, 3000, 3000, 3000, 3000, 3000}
	if vfr {
		durs = []int64{1000, 4500, 2200, 6000, 1500, 3800, 2900, 5100}
	}
	var t int64
	for i, d := range durs {
		payload := avccSample(r, i%3 == 0)
		events = append(events, model.Event{
			TrackID: "mp4:1", Kind: model.EventMedia, Ticks: t, Duration: d,
			Size: uint32(len(payload)), OutTrackID: "out:v", Payload: payload,
		})
		t += d
	}
	for i := 0; i < 4; i++ {
		cue := []byte(fmt.Sprintf("cue-%d seed-ok", i))
		events = append(events, model.Event{
			TrackID: "mp4:2", Kind: model.EventSubtitle,
			Ticks: subShift + int64(i)*15000, Duration: 4500,
			Size: uint32(len(cue)), OutTrackID: "out:s", Payload: cue,
		})
	}
	for i := 0; i < 3; i++ {
		id3 := append([]byte("ID3\x04\x00"), randBytes(r, 10)...)
		events = append(events, model.Event{
			TrackID: "mp4:3", Kind: model.EventID3, Ticks: int64(i) * 30000,
			Size: uint32(len(id3)), OutTrackID: "out:m", Payload: id3,
		})
	}
	for i, title := range []string{"Chapter 1", "Chapter 2"} {
		events = append(events, model.Event{
			TrackID: "mp4:chpl", Kind: model.EventChapter, Ticks: int64(i) * 60000,
			Size: uint32(len(title)), OutTrackID: "out:m", Payload: []byte(title),
		})
	}
	for i := 0; i < 2; i++ {
		msg := []byte(fmt.Sprintf("emsg-%d", i))
		events = append(events, model.Event{
			TrackID: "mp4:emsg", Kind: model.EventEMSG, Ticks: 10000 + int64(i)*40000,
			Duration: 1000, Size: uint32(len(msg)), OutTrackID: "out:m", Payload: msg,
		})
	}
	return events
}

func writeMP4(path string, events []model.Event) error {
	return remux.WriteMP4(path, OutputTracks(), events)
}

// Generate writes all sample files into dir, deterministically from seed.
func Generate(dir string, seed int64) ([]Sample, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var samples []Sample
	add := func(name, path, kind string, ok bool, note string) Sample {
		s := Sample{Name: name, Path: path, Kind: kind, ExpectOK: ok, Note: note}
		samples = append(samples, s)
		return s
	}
	// normal.mp4
	r := rand.New(rand.NewSource(seed))
	normal := filepath.Join(dir, "normal.mp4")
	if err := writeMP4(normal, mediaEvents(r, 0, false)); err != nil {
		return nil, err
	}
	add("normal", normal, "mp4", true, "")
	// vfr.mp4: variable frame rate video.
	r = rand.New(rand.NewSource(seed + 1))
	vfr := filepath.Join(dir, "vfr.mp4")
	if err := writeMP4(vfr, mediaEvents(r, 0, true)); err != nil {
		return nil, err
	}
	add("vfr", vfr, "mp4", true, "variable frame rate")
	// drift.mp4: subtitle track offset beyond the default threshold.
	r = rand.New(rand.NewSource(seed + 2))
	drift := filepath.Join(dir, "drift.mp4")
	if err := writeMP4(drift, mediaEvents(r, 200000, false)); err != nil {
		return nil, err
	}
	add("drift", drift, "mp4", false, "subtitle drift exceeds threshold")
	// truncated.mp4: normal cut short inside mdat.
	raw, err := os.ReadFile(normal)
	if err != nil {
		return nil, err
	}
	trunc := filepath.Join(dir, "truncated.mp4")
	if err := os.WriteFile(trunc, raw[:len(raw)-97], 0o644); err != nil {
		return nil, err
	}
	add("truncated", trunc, "mp4", false, "mdat cut short")
	// badlen.mp4: absurd top-level box length.
	bad := make([]byte, len(raw))
	copy(bad, raw)
	binary.BigEndian.PutUint32(bad[0:4], 0x7fffffff)
	badlen := filepath.Join(dir, "badlen.mp4")
	if err := os.WriteFile(badlen, bad, 0o644); err != nil {
		return nil, err
	}
	add("badlen", badlen, "mp4", false, "box length corrupted")
	// wrap.rtp: RTP dump crossing sequence and timestamp wrap points.
	wrap := filepath.Join(dir, "wrap.rtp")
	if err := writeRTPWrap(wrap, rand.New(rand.NewSource(seed+3))); err != nil {
		return nil, err
	}
	add("wrap", wrap, "rtp", true, "rtp timestamp/sequence wrap")
	return samples, nil
}

func writeRTPWrap(path string, r *rand.Rand) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	seqs := []uint16{65532, 65533, 65534, 65535, 0, 1, 2, 3}
	base := uint32(0xfffffff0)
	for i, s := range seqs {
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96,
				SequenceNumber: s,
				Timestamp:      base + uint32(i)*3000,
				SSRC:           0x17285,
			},
			Payload: randBytes(r, 16),
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return err
		}
		var lb [2]byte
		binary.BigEndian.PutUint16(lb[:], uint16(len(raw)))
		if _, err := f.Write(lb[:]); err != nil {
			return err
		}
		if _, err := f.Write(raw); err != nil {
			return err
		}
	}
	return nil
}
