// Package fixture generates the deterministic MXF and MP4 sample containers
// used by the demo and the tests. Nothing binary is stored in the repository:
// every byte is produced from a declarative spec so a damaged copy can be
// regenerated wherever it is needed.
package fixture

import (
	"fmt"
	"sort"

	"archive/internal/essence"
	"archive/internal/model"
)

// ColorClaim is one colour value as it will be physically embedded in a
// container. Multiple containers may claim different values for the same
// logical track, which is exactly the conflict the probe must arbitrate.
type ColorClaim struct {
	Primaries string
	Transfer  string
	Matrix    string
	FullRange bool
	Subsample string // chroma subsampling, metadata only
}

// TrackSpec describes one logical track of the fixture.
type TrackSpec struct {
	Logical    string
	Container  model.ContainerKind
	TrackID    uint32
	Kind       model.TrackKind
	Codec      string
	Timescale  uint32
	FrameTicks uint64
	Frames     int
	StartTick  uint64
	PayloadLen int
	SyncEvery  int
	Required   bool
	Rotation   int
	Crop       *model.Crop
	Color      ColorClaim
	// DropsIndex omits the sample table / index while leaving media intact.
	DropsIndex bool
	// CarriesRTP embeds an RTP header in each frame payload, as a
	// stream-captured master would. The RTP timestamp is a second statement
	// of media time that the demux cross-checks against the container.
	CarriesRTP bool
	// DependsOn lists logical tracks this one needs before mapping.
	DependsOn []string
}

// Spec is one generated fixture pair (one container plus its tracks).
type Spec struct {
	Name   string
	Format model.ContainerKind
	Tracks []TrackSpec
	Co64   bool // MP4 only: use 64-bit chunk offsets
}

// FrameLayout is one frame's position inside the media area.
type FrameLayout struct {
	Seq       uint32
	Offset    uint64 // absolute offset in the file
	Size      uint64
	StartTick uint64
	Duration  uint64
	Sync      bool
}

// TrackLayout is the generated truth for one track.
type TrackLayout struct {
	Spec      TrackSpec
	Frames    []FrameLayout
	MediaBase uint64
	MediaSize uint64
}

// Ends at the first tick after the last frame.
func (t *TrackLayout) EndTick() uint64 {
	return t.Spec.StartTick + uint64(t.Spec.Frames)*t.Spec.FrameTicks
}

// Layout is the full generated truth for one container file.
type Layout struct {
	Spec       Spec
	Tracks     []*TrackLayout
	Size       uint64
	MediaStart uint64
	MediaEnd   uint64
}

// DefaultMixed returns the mixed MXF+MP4 fixture used by the demo. Both
// containers describe the same logical asset (video, audio and, in the MXF,
// data), which is how a legacy archive ends up with several container
// versions of one reel. The two containers deliberately disagree about the
// colour description so arbitration has a real conflict to resolve: MXF
// says BT.709 while MP4 says BT.2020/PQ. Per-track crop and rotation are
// physical properties and stay with their container.
func DefaultMixed() []Spec {
	crop16x9 := &model.Crop{Left: 0, Top: 0, Right: 1920, Bottom: 1080}
	return []Spec{{
		Name:   "reel_a",
		Format: model.ContainerMXF,
		Tracks: []TrackSpec{
			{
				Logical: "video", Container: model.ContainerMXF, TrackID: 1,
				Kind: model.KindVideo, Codec: "mxf-h264", Timescale: 25000,
				FrameTicks: 1000, Frames: 8, PayloadLen: 96, SyncEvery: 4,
				Required: true, Rotation: 90, Crop: crop16x9, CarriesRTP: true,
				Color: ColorClaim{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", FullRange: false},
			},
			{
				Logical: "audio", Container: model.ContainerMXF, TrackID: 2,
				Kind: model.KindAudio, Codec: "mxf-pcm", Timescale: 48000,
				FrameTicks: 2000, Frames: 10, PayloadLen: 64, SyncEvery: 1,
				Required: true,
				Color:    ColorClaim{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709"},
			},
			{
				Logical: "data", Container: model.ContainerMXF, TrackID: 3,
				Kind: model.KindData, Codec: "mxf-meta", Timescale: 1000,
				FrameTicks: 500, Frames: 6, PayloadLen: 32, SyncEvery: 1,
				Color:     ColorClaim{Primaries: "unspecified", Transfer: "unspecified", Matrix: "unspecified"},
				DependsOn: []string{"video"},
			},
		},
	}, {
		Name:   "clip_b",
		Format: model.ContainerMP4,
		Tracks: []TrackSpec{
			{
				Logical: "video", Container: model.ContainerMP4, TrackID: 1,
				Kind: model.KindVideo, Codec: "avc1", Timescale: 90000,
				FrameTicks: 3600, Frames: 8, PayloadLen: 120, SyncEvery: 4,
				Required: true, Rotation: 270,
				// The MP4 copy was cropped to 9:16 for a vertical
				// deliverable; the MXF master still carries 16:9. Crop is
				// container-local and must not be arbitrated away.
				Crop:  &model.Crop{Left: 0, Top: 0, Right: 1080, Bottom: 1920},
				Color: ColorClaim{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc", FullRange: true},
			},
			{
				Logical: "audio", Container: model.ContainerMP4, TrackID: 2,
				Kind: model.KindAudio, Codec: "mp4a", Timescale: 48000,
				FrameTicks: 1024, Frames: 12, PayloadLen: 80, SyncEvery: 1,
				Required: true,
				Color:    ColorClaim{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709"},
			},
		},
	}}
}

// DefaultFull returns a fixture with complete indexes, used by verification
// tests that expect a clean run.
func DefaultFull() []Spec {
	specs := DefaultMixed()
	for si := range specs {
		for ti := range specs[si].Tracks {
			specs[si].Tracks[ti].DropsIndex = false
		}
	}
	return specs
}

// FindSpec returns a spec by name.
func FindSpec(specs []Spec, name string) (Spec, bool) {
	for _, s := range specs {
		if s.Name == name {
			return s, true
		}
	}
	return Spec{}, false
}

// Validate checks a spec before any bytes are written.
func (s Spec) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("fixture: empty spec name")
	}
	if s.Format != model.ContainerMP4 && s.Format != model.ContainerMXF {
		return fmt.Errorf("fixture: unsupported format %q", s.Format)
	}
	seen := make(map[string]bool, len(s.Tracks))
	for i, t := range s.Tracks {
		if t.Logical == "" || t.TrackID == 0 {
			return fmt.Errorf("fixture: track %d needs a logical name and id", i)
		}
		if seen[t.Logical] {
			return fmt.Errorf("fixture: duplicate logical track %q", t.Logical)
		}
		seen[t.Logical] = true
		if t.Timescale == 0 || t.FrameTicks == 0 || t.Frames <= 0 {
			return fmt.Errorf("fixture: track %q needs a positive timebase and frames", t.Logical)
		}
		if t.PayloadLen < 8 {
			return fmt.Errorf("fixture: track %q payload too small", t.Logical)
		}
	}
	return nil
}

// LogicalKeys lists the logical track names in a stable order.
func (s Spec) LogicalKeys() []string {
	keys := make([]string, 0, len(s.Tracks))
	for _, t := range s.Tracks {
		keys = append(keys, t.Logical)
	}
	sort.Strings(keys)
	return keys
}

// EssenceKind maps the model kind to the frame kind byte.
func EssenceKind(k model.TrackKind) essence.Kind {
	switch k {
	case model.KindAudio:
		return essence.Audio
	case model.KindData:
		return essence.Data
	default:
		return essence.Video
	}
}

// BuildFrame serialises frame i of the given track. The payload seed mixes the
// logical name, track id and frame sequence so identical runs produce
// identical bytes while different tracks stay distinguishable.
func BuildFrame(t TrackSpec, i int) []byte {
	seq := uint32(i)
	start := t.StartTick + uint64(i)*t.FrameTicks
	sync := t.SyncEvery <= 1 || i%t.SyncEvery == 0
	var flags uint16
	if sync {
		flags |= essence.FlagSync
	}
	if t.CarriesRTP {
		flags |= essence.FlagRTP
	}
	payload := essence.Payload(seedFor(t.Logical, t.TrackID, seq), t.PayloadLen)
	if t.CarriesRTP {
		rtpHeader := essence.RPTSample(uint16(seq), start, t.TrackID, t.Kind == model.KindAudio)
		hdr, err := rtpHeader.Marshal()
		if err != nil {
			panic("fixture: RTP header: " + err.Error())
		}
		payload = append(hdr, payload...)
	}
	return essence.Build(essence.Header{
		Kind:      EssenceKind(t.Kind),
		Flags:     flags,
		TrackID:   t.TrackID,
		Seq:       seq,
		StartTick: start,
		Duration:  t.FrameTicks,
	}, payload)
}

func seedFor(name string, id, seq uint32) uint64 {
	h := uint64(1469598103934665603)
	for i := 0; i < len(name); i++ {
		h ^= uint64(name[i])
		h *= 1099511628211
	}
	h ^= uint64(id) * 131
	h ^= uint64(seq) * 2654435761
	return h
}

// FrameCount is the total number of frames across all tracks.
func (s Spec) FrameCount() int {
	n := 0
	for _, t := range s.Tracks {
		n += t.Frames
	}
	return n
}
