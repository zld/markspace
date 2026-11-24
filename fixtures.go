package mediacore

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"os"

	mp4 "github.com/abema/go-mp4"
)

// memSeeker is a tiny in-memory io.WriteSeeker used to size boxes before
// writing them to disk.
type memSeeker struct {
	buf []byte
	pos int64
}

func (m *memSeeker) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if end > int64(len(m.buf)) {
		grown := make([]byte, end)
		copy(grown, m.buf)
		m.buf = grown
	}
	copy(m.buf[m.pos:end], p)
	m.pos = end
	return len(p), nil
}

func (m *memSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		m.pos = offset
	case 1:
		m.pos += offset
	case 2:
		m.pos = int64(len(m.buf)) + offset
	}
	if m.pos < 0 {
		return 0, fmt.Errorf("negative seek position")
	}
	return m.pos, nil
}

func (m *memSeeker) Bytes() []byte { return m.buf }

// SampleSpec describes one synthetic sample in a fixture track.
type SampleSpec struct {
	Data []byte
	DTS  int64
	Dur  uint32
	CTS  int64
	Sync bool
}

// TrackSpec describes one synthetic track for fixture generation.
type TrackSpec struct {
	ID        uint32
	Kind      TrackKind
	Handler   string
	Timescale uint32
	Codec     string
	Samples   []SampleSpec
	Width     uint16
	Height    uint16
	Channels  uint16
}

// FixturePaths names the generated sample set.
type FixturePaths struct {
	MP4       string
	MOV       string
	Truncated string
	BadOffset string
	Unknown   string
	Mutated   string
}

// GenerateFixtures creates a deterministic fixture set under dir.
func GenerateFixtures(dir string, seed int64) (*FixturePaths, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, wrapErr(KindIO, "fixtures", err)
	}
	p := &FixturePaths{
		MP4:       dir + "/normal.mp4",
		MOV:       dir + "/normal.mov",
		Truncated: dir + "/truncated.mp4",
		BadOffset: dir + "/bad_offset.mp4",
		Unknown:   dir + "/unknown.mp4",
		Mutated:   dir + "/mutated.mp4",
	}
	tracks := syntheticTracks(seed, false)
	if err := writeFixtureMP4(p.MP4, "isom", tracks); err != nil {
		return nil, err
	}
	if err := writeFixtureMP4(p.MOV, "qt  ", tracks); err != nil {
		return nil, err
	}
	unknown := syntheticTracks(seed, true)
	if err := writeFixtureMP4(p.Unknown, "isom", unknown); err != nil {
		return nil, err
	}
	if err := truncateMoov(p.MP4, p.Truncated); err != nil {
		return nil, err
	}
	if err := corruptChunkOffset(p.MP4, p.BadOffset); err != nil {
		return nil, err
	}
	if err := mutateMediaByte(p.MP4, p.Mutated); err != nil {
		return nil, err
	}
	return p, nil
}

// syntheticTracks builds the deterministic video/audio tracks used across
// the demo and tests. withUnknown adds an opaque metadata track.
func syntheticTracks(seed int64, withUnknown bool) []TrackSpec {
	rnd := rand.New(rand.NewSource(seed))
	video := TrackSpec{
		ID: 1, Kind: KindVideoTrack, Handler: "vide", Timescale: 90000,
		Codec: "avc1", Width: 320, Height: 240,
	}
	audio := TrackSpec{
		ID: 2, Kind: KindAudioTrack, Handler: "soun", Timescale: 48000,
		Codec: "mp4a", Channels: 2,
	}
	var dts int64
	for i := 0; i < 40; i++ {
		size := 220 + rnd.Intn(90)
		data := make([]byte, size)
		for j := range data {
			data[j] = byte(0xA0 + (i*17+j*31)%64)
		}
		// Every second sample carries a positive composition offset, which
		// exercises ctts reconstruction without violating DTS monotonicity.
		cts := dts
		if i%2 == 1 {
			cts = dts + 1500
		}
		video.Samples = append(video.Samples, SampleSpec{
			Data: data, DTS: dts, Dur: 3000, CTS: cts, Sync: i%4 == 0,
		})
		dts += 3000
	}
	dts = 0
	for i := 0; i < 64; i++ {
		size := 96 + rnd.Intn(32)
		data := make([]byte, size)
		for j := range data {
			data[j] = byte(0x40 + (i*13+j*7)%64)
		}
		audio.Samples = append(audio.Samples, SampleSpec{
			Data: data, DTS: dts, Dur: 1024, CTS: dts, Sync: true,
		})
		dts += 1024
	}
	out := []TrackSpec{video, audio}
	if withUnknown {
		meta := TrackSpec{ID: 3, Kind: KindOtherTrack, Handler: "meta", Timescale: 1000, Codec: "avc1",
			Width: 16, Height: 16}
		for i := 0; i < 3; i++ {
			meta.Samples = append(meta.Samples, SampleSpec{
				Data: []byte(fmt.Sprintf("opaque-%d", i)), DTS: int64(i * 500), Dur: 500, CTS: int64(i * 500), Sync: true,
			})
		}
		out = append(out, meta)
	}
	return out
}

// writeFixtureMP4 lays out ftyp + moov + mdat. The mdat is placed first so
// the moov sample offsets are known without a second sizing pass; this also
// exercises non-fast-start layout, the common case for camera output.
func writeFixtureMP4(path, brand string, tracks []TrackSpec) error {
	ftyp, err := buildFtyp(brand)
	if err != nil {
		return err
	}
	payload := fixturePaylod(tracks)
	mux := toMuxTracks(tracks)
	// mdat header + payload follow ftyp directly.
	mdatBase := int64(len(ftyp)) + 8
	var chunks []muxChunk
	chunks, end := planMuxLayout(mux, mdatBase)
	if end-mdatBase != int64(len(payload)) {
		return fail(KindInternal, "fixtures", "payload plan mismatch (%d != %d)", end-mdatBase, len(payload))
	}
	moov, err := buildMoov(mux, chunks, 1000, int64(fixtureMovieDuration(mux)))
	if err != nil {
		return err
	}
	out := make([]byte, 0, int64(len(ftyp))+8+int64(len(payload))+int64(len(moov)))
	out = append(out, ftyp...)
	out = append(out, boxHeader(mp4.BoxTypeMdat(), int64(len(payload))+8)...)
	out = append(out, payload...)
	out = append(out, moov...)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return wrapErr(KindIO, "fixtures", err)
	}
	return nil
}

func boxHeader(typ mp4.BoxType, size int64) []byte {
	h := make([]byte, 8)
	binary.BigEndian.PutUint32(h, uint32(size))
	copy(h[4:], []byte{byte(typ[0]), byte(typ[1]), byte(typ[2]), byte(typ[3])})
	return h
}

func fixturePaylod(tracks []TrackSpec) []byte {
	var out []byte
	for _, tr := range tracks {
		for _, sm := range tr.Samples {
			out = append(out, sm.Data...)
		}
	}
	return out
}

func toMuxTracks(tracks []TrackSpec) []muxTrack {
	out := make([]muxTrack, len(tracks))
	for i, tr := range tracks {
		m := muxTrack{ID: tr.ID, Kind: tr.Kind, Handler: tr.Handler, Timescale: tr.Timescale,
			Width: tr.Width, Height: tr.Height, Channels: tr.Channels}
		for _, sm := range tr.Samples {
			m.Samples = append(m.Samples, muxSample{Size: int64(len(sm.Data)), DTS: sm.DTS, Dur: sm.Dur, CTS: sm.CTS, Sync: sm.Sync})
		}
		out[i] = m
	}
	return out
}

func fixtureMovieDuration(tracks []muxTrack) int64 {
	var max int64
	for _, tr := range tracks {
		d, err := Rescale(trackEnd(tr), tr.Timescale, 1000)
		if err != nil {
			continue
		}
		if d > max {
			max = d
		}
	}
	return max
}

func buildFtyp(brand string) ([]byte, error) {
	ms := &memSeeker{}
	w := mp4.NewWriter(ms)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, wrapErr(KindInternal, "fixtures", err)
	}
	major := [4]byte{brand[0], brand[1], brand[2], brand[3]}
	compat := []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}, {CompatibleBrand: major}}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{MajorBrand: major, CompatibleBrands: compat}, mp4.Context{}); err != nil {
		return nil, wrapErr(KindInternal, "fixtures", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, wrapErr(KindInternal, "fixtures", err)
	}
	return ms.Bytes(), nil
}

// bytesSeeker adapts a byte slice to io.ReadSeeker.
type byteSeeker struct {
	b []byte
	p int64
}

func bytesSeeker(b []byte) *byteSeeker { return &byteSeeker{b: b} }

func (s *byteSeeker) Read(p []byte) (int, error) {
	if s.p >= int64(len(s.b)) {
		return 0, io.EOF
	}
	n := copy(p, s.b[s.p:])
	s.p += int64(n)
	return n, nil
}

func (s *byteSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		s.p = offset
	case 1:
		s.p += offset
	case 2:
		s.p = int64(len(s.b)) + offset
	}
	if s.p < 0 {
		return 0, fail(KindIO, "fixtures", "negative seek")
	}
	return s.p, nil
}
