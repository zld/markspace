package fixture

import (
	"encoding/binary"
	"fmt"
	"math/rand"

	"mediarc/internal/model"
)

type Options struct {
	Seed          int64
	LossPackets   int
	LateMS        int64
	ColorConflict bool
	BrandMOV      bool
	PacketCount   int
}

func DefaultOptions() Options {
	return Options{Seed: 7, LossPackets: 8, LateMS: 120, PacketCount: 48}
}

// GenerateArchive builds all synthetic source data in memory. It is fully
// deterministic for the supplied options and seed.
func GenerateArchive(opt Options) *model.Archive {
	if opt.PacketCount <= 0 {
		opt.PacketCount = 48
	}
	rng := rand.New(rand.NewSource(opt.Seed))
	brand := "MP4"
	if opt.BrandMOV {
		brand = "MOV"
	}
	a := &model.Archive{
		Brand:       brand,
		GeneratedBy: fmt.Sprintf("mediarc-fixture seed=%d loss=%d late=%d", opt.Seed, opt.LossPackets, opt.LateMS),
	}

	vBase := model.TimeBase{Num: 1, Den: 30000}
	a.Tracks = append(a.Tracks, model.Track{
		ID: 1, Type: model.TrackVideo, Codec: model.CodecSyntheticVideo,
		TimeBase: vBase, DurationTicks: 30000, Width: 320, Height: 180, Required: true,
		Color: &model.ColorMeta{Primaries: 1, Transfer: 1, Matrix: 1, FullRange: false, SourceDescription: "mp4.colr"},
	})
	a.Tracks = append(a.Tracks, model.Track{
		ID: 2, Type: model.TrackAudio, Codec: model.CodecSyntheticAudio,
		TimeBase: model.TimeBase{Num: 1, Den: 48000}, DurationTicks: 49152,
		SampleRate: 48000, Channels: 2, Layout: model.LayoutStereo, Required: true,
	})
	a.Tracks = append(a.Tracks, model.Track{ID: 3, Type: model.TrackSubtitle, Codec: model.CodecWebVTT, TimeBase: model.TimeBase{Num: 1, Den: 1000}, DurationTicks: 1000})
	a.Tracks = append(a.Tracks, model.Track{ID: 4, Type: model.TrackChapter, Codec: model.CodecChapter, TimeBase: model.TimeBase{Num: 1, Den: 1000}, DurationTicks: 1000})
	a.Tracks = append(a.Tracks, model.Track{ID: 5, Type: model.TrackMetadata, Codec: model.CodecEmsg, TimeBase: model.TimeBase{Num: 1, Den: 1000}, DurationTicks: 1000})

	if opt.ColorConflict {
		a.Tracks[0].ColorAlternatives = []model.ColorMeta{
			*a.Tracks[0].Color,
			{Primaries: 9, Transfer: 16, Matrix: 9, FullRange: true, SourceDescription: "h264.vui"},
		}
	}

	for i := 0; i < 30; i++ {
		payload := []byte(fmt.Sprintf("SYNV seed=%d frame=%02d ", opt.Seed, i))
		payload = append(payload, deterministicBytes(rng, 24)...)
		a.Samples = append(a.Samples, model.Sample{
			TrackID: 1, Index: i, PTS: int64(i * 1000), DTS: int64(i * 1000),
			Duration: 1000, KeyFrame: i%6 == 0, Payload: payload,
		})
	}
	for i := 0; i < 48; i++ {
		payload := []byte(fmt.Sprintf("SYNA seed=%d chunk=%02d ", opt.Seed, i))
		payload = append(payload, deterministicBytes(rng, 16)...)
		a.Samples = append(a.Samples, model.Sample{
			TrackID: 2, Index: i, PTS: int64(i * 1024), DTS: int64(i * 1024),
			Duration: 1024, KeyFrame: true, Payload: payload,
		})
	}

	a.Events = buildEvents(opt)
	a.RTPPackets = buildRTPPackets(opt, rng)
	a.ProcessingLatenciesMS = buildLatencies(opt.Seed)
	return a
}

func deterministicBytes(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	_, _ = rng.Read(b)
	return b
}

func buildEvents(opt Options) []model.TimedEvent {
	events := []model.TimedEvent{
		{ID: "chapters", Kind: model.MetaChapter, TrackID: 4, PTS: model.Milliseconds(0), SourceOffset: 4096,
			Payload: ChapterBook([]Chapter{{0, "Opening"}, {300, "Archive reel"}, {700, "Recovery"}})},
		{ID: "id3-tit2", Kind: model.MetaID3, TrackID: 5, PTS: model.Milliseconds(20), SourceOffset: 4200,
			Payload: ID3Tag("Historical Asset 7", "MediaRC")},
		{ID: "sub-001", Kind: model.MetaSubtitle, TrackID: 3, PTS: model.Milliseconds(100), DurationMS: 2000, SourceOffset: 8192,
			Payload: WebVTT(100, "Deterministic archive recovery")},
	}
	if msg, err := Emsg(250, 1001, "timed metadata beacon"); err == nil {
		events = append(events, model.TimedEvent{ID: "emsg-1001", Kind: model.MetaEmsg, TrackID: 5,
			PTS: model.Milliseconds(250), SourceOffset: 12288, Payload: msg})
	}
	events = append(events, model.TimedEvent{ID: "sei-user", Kind: model.MetaSEI, TrackID: 1,
		PTS: model.Milliseconds(333), SourceOffset: 16384, Payload: SEIUserdata("frame synchronization marker")})
	latePTS := int64(720)
	events = append(events, model.TimedEvent{ID: "sub-late", Kind: model.MetaSubtitle, TrackID: 3,
		PTS: model.Milliseconds(latePTS), DurationMS: 1500, SourceOffset: 20480,
		Payload: WebVTT(latePTS, "late but recoverable metadata")})
	return events
}

func buildLatencies(seed int64) []int64 {
	r := rand.New(rand.NewSource(seed ^ 0x9e3779b9))
	out := make([]int64, 120)
	for i := range out {
		out[i] = 1 + int64(r.Intn(18)) // synthetic local processing milliseconds
	}
	return out
}

func sequencePayload(seed int64, i int) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint64(b[:8], uint64(seed))
	binary.BigEndian.PutUint32(b[8:], uint32(i))
	return append([]byte(fmt.Sprintf("RTP-%04d-", i)), b...)
}
