// Package matroska adapts the raw EBML tree into the container-neutral media
// model: doc type, track metadata, cluster timing and interleaved blocks, all
// converted to the shared nanosecond time base with raw offsets preserved.
package matroska

import (
	"fmt"
	"io"
	"os"

	"example.com/assetcheck/internal/ebml"
	"example.com/assetcheck/internal/media"
	"example.com/assetcheck/internal/timeline"
	"example.com/assetcheck/internal/tracks"
)

// Parse reads a Matroska or WebM file and returns the media model. It parses the
// element tree first (streaming, no payload copies) and then reads only the
// small metadata leaves it needs.
func Parse(path string) (*media.Asset, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, 0, -1,
			"open %s", path)
	}
	defer f.Close()

	hash, size, err := media.HashFile(f)
	if err != nil {
		return nil, err
	}
	tree, err := ebml.ParseTree(f, ebml.DefaultLimits())
	if err != nil {
		return nil, err
	}

	a := &media.Asset{
		Path:         path,
		Size:         size,
		SHA256:       hash,
		Clock:        timeline.MustClock(1),
		Version:      0,
		SequenceBase: -1,
	}
	if err := applyHeader(f, tree, a); err != nil {
		return nil, err
	}
	if err := applyInfo(f, tree, a); err != nil {
		return nil, err
	}
	if err := applyTracks(f, tree, a); err != nil {
		return nil, err
	}
	if err := applyFragments(f, tree, a); err != nil {
		return nil, err
	}
	recordElements(tree, a)
	tracks.SortTracks(a.Tracks)
	return a, nil
}

func applyHeader(src io.ReadSeeker, tree *ebml.Tree, a *media.Asset) error {
	head := tree.Root.Child(ebml.IDEBML)
	if head == nil {
		return timeline.New(timeline.StageParse, timeline.CatMissingElement, ebml.IDEBML, 0,
			"file does not start with an EBML header")
	}
	docType, err := ebml.ReadString(src, head.Child(ebml.IDDocType))
	if err != nil {
		return err
	}
	switch docType {
	case "matroska":
		a.Container = media.ContainerMatroska
	case "webm":
		a.Container = media.ContainerWebM
	case "":
		return timeline.New(timeline.StageParse, timeline.CatMissingElement, ebml.IDDocType, head.Offset,
			"EBML header has no DocType")
	default:
		return timeline.New(timeline.StageParse, timeline.CatUnsupported, ebml.IDDocType, head.Offset,
			"unsupported DocType %q", docType)
	}
	a.DocType = docType
	a.Version, err = ebml.ReadUintChild(src, head, ebml.IDDocTypeVersion)
	if err != nil {
		return err
	}
	if a.Version == 0 {
		a.Version = 1
	}
	if a.Version > 4 {
		return timeline.New(timeline.StageParse, timeline.CatUnsupported, ebml.IDDocTypeVersion,
			head.Offset, "DocTypeVersion %d is newer than supported 4", a.Version)
	}
	return nil
}

func applyInfo(src io.ReadSeeker, tree *ebml.Tree, a *media.Asset) error {
	info := tree.Root.Child(ebml.IDSegment)
	if info == nil {
		return timeline.New(timeline.StageParse, timeline.CatMissingElement, ebml.IDSegment, 0,
			"file has no Segment")
	}
	seg := info
	info = seg.Child(ebml.IDInfo)
	if info == nil {
		return timeline.New(timeline.StageParse, timeline.CatMissingElement, ebml.IDInfo, seg.Offset,
			"Segment has no Info element")
	}
	scale := DefaultTimecodeScale
	if n := info.Child(ebml.IDTimecodeScale); n != nil {
		v, err := ebml.ReadUint(src, n)
		if err != nil {
			return err
		}
		if v == 0 {
			return timeline.New(timeline.StageParse, timeline.CatIllegalLength, n.ID, n.Offset,
				"TimecodeScale is zero")
		}
		scale = v
	}
	a.TimecodeScale = scale
	// The shared clock runs on nanoseconds, so a Matroska tick of `scale`
	// nanoseconds is handled by tickDuration rather than by the clock.
	a.Clock = timeline.MustClock(1_000_000_000)

	if n := info.Child(ebml.IDDuration); n != nil {
		d, err := ebml.ReadFloat(src, n)
		if err != nil {
			return err
		}
		if d > 0 {
			a.Duration = tickDuration(d, scale)
		}
	}
	return nil
}

// DefaultTimecodeScale is the Matroska default of one millisecond per tick.
const DefaultTimecodeScale uint64 = 1_000_000

// tickDuration converts a Matroska duration expressed in ticks (passed as float)
// to nanoseconds, saturating rather than overflowing.
func tickDuration(ticks float64, scale uint64) timeline.Nanosecond {
	ns := ticks * float64(scale)
	if ns <= 0 {
		return 0
	}
	if ns >= float64(1<<62) {
		return timeline.Nanosecond(1 << 62)
	}
	return timeline.Nanosecond(ns)
}

func applyTracks(src io.ReadSeeker, tree *ebml.Tree, a *media.Asset) error {
	seg := tree.Root.Child(ebml.IDSegment)
	if seg == nil {
		return timeline.New(timeline.StageParse, timeline.CatMissingElement, ebml.IDSegment, 0,
			"file has no Segment")
	}
	trackList := seg.Child(ebml.IDTracks)
	if trackList == nil {
		return timeline.New(timeline.StageParse, timeline.CatMissingElement, ebml.IDTracks, seg.Offset,
			"Segment has no Tracks element")
	}
	if len(trackList.ChildrenOf(ebml.IDTrackEntry)) == 0 {
		return timeline.New(timeline.StageParse, timeline.CatMissingElement, ebml.IDTrackEntry,
			trackList.Offset, "Tracks element has no TrackEntry")
	}
	for _, entry := range trackList.ChildrenOf(ebml.IDTrackEntry) {
		tr, err := parseTrackEntry(src, entry)
		if err != nil {
			return err
		}
		a.Tracks = append(a.Tracks, tr)
	}
	return nil
}

func parseTrackEntry(src io.ReadSeeker, entry *ebml.Node) (tracks.Track, error) {
	num, err := ebml.RequireUint(src, entry, ebml.IDTrackNumber)
	if err != nil {
		return tracks.Track{}, err
	}
	if num == 0 {
		return tracks.Track{}, timeline.New(timeline.StageParse, timeline.CatIllegalLength,
			ebml.IDTrackNumber, entry.Offset, "TrackNumber is zero")
	}
	typRaw, err := ebml.RequireUint(src, entry, ebml.IDTrackType)
	if err != nil {
		return tracks.Track{}, err
	}
	typ := tracks.TrackType(typRaw)
	if !typ.Valid() {
		return tracks.Track{}, timeline.New(timeline.StageParse, timeline.CatUnsupported,
			ebml.IDTrackType, entry.Offset, "track %d has unsupported TrackType %d", num, typRaw)
	}
	codec, err := ebml.ReadString(src, entry.Child(ebml.IDCodecID))
	if err != nil {
		return tracks.Track{}, err
	}
	if codec == "" {
		return tracks.Track{}, timeline.New(timeline.StageParse, timeline.CatMissingElement,
			ebml.IDCodecID, entry.Offset, "track %d has no CodecID", num)
	}
	lang, err := ebml.ReadString(src, entry.Child(ebml.IDLanguage))
	if err != nil {
		return tracks.Track{}, err
	}
	if lang == "" {
		lang = "und"
	}
	name, err := ebml.ReadString(src, entry.Child(ebml.IDName))
	if err != nil {
		return tracks.Track{}, err
	}
	uid, err := ebml.ReadUintChild(src, entry, ebml.IDTrackUID)
	if err != nil {
		return tracks.Track{}, err
	}
	dur, err := ebml.ReadUintChild(src, entry, ebml.IDDefaultDuration)
	if err != nil {
		return tracks.Track{}, err
	}

	tr := tracks.Track{
		Number: num, UID: uid, Type: typ, CodecID: codec, Language: lang,
		Name: name, Offset: entry.Offset, DefaultDuration: dur,
	}
	switch typ {
	case tracks.TypeAudio:
		audioNode := entry.Child(ebml.IDAudio)
		if audioNode == nil {
			return tracks.Track{}, timeline.New(timeline.StageParse, timeline.CatMissingElement,
				ebml.IDAudio, entry.Offset, "audio track %d has no Audio element", num)
		}
		ch, err := ebml.RequireUint(src, audioNode, ebml.IDChannels)
		if err != nil {
			return tracks.Track{}, err
		}
		rate, err := ebml.ReadFloat(src, audioNode.Child(ebml.IDSamplingFrequency))
		if err != nil {
			return tracks.Track{}, err
		}
		outRate, err := ebml.ReadFloat(src, audioNode.Child(ebml.IDOutputSamplingFreq))
		if err != nil {
			return tracks.Track{}, err
		}
		if rate == 0 {
			rate = 8000 // Matroska default when the float is absent
		}
		depth, err := ebml.ReadUintChild(src, audioNode, ebml.IDBitDepth)
		if err != nil {
			return tracks.Track{}, err
		}
		tr.Audio = &tracks.Audio{
			Channels: ch, SamplingFrequency: rate, OutputSamplingFreq: outRate,
			BitDepth: depth, Layout: layoutFor(ch),
		}
	case tracks.TypeVideo:
		videoNode := entry.Child(ebml.IDVideo)
		if videoNode == nil {
			return tracks.Track{}, timeline.New(timeline.StageParse, timeline.CatMissingElement,
				ebml.IDVideo, entry.Offset, "video track %d has no Video element", num)
		}
		v, err := parseVideo(src, videoNode)
		if err != nil {
			return tracks.Track{}, err
		}
		tr.Video = v
	}
	return tr, nil
}

func parseVideo(src io.ReadSeeker, node *ebml.Node) (*tracks.Video, error) {
	w, err := ebml.RequireUint(src, node, ebml.IDPixelWidth)
	if err != nil {
		return nil, err
	}
	h, err := ebml.RequireUint(src, node, ebml.IDPixelHeight)
	if err != nil {
		return nil, err
	}
	dw, err := ebml.ReadUintChild(src, node, ebml.IDDisplayWidth)
	if err != nil {
		return nil, err
	}
	dh, err := ebml.ReadUintChild(src, node, ebml.IDDisplayHeight)
	if err != nil {
		return nil, err
	}
	v := &tracks.Video{PixelWidth: w, PixelHeight: h, DisplayWidth: dw, DisplayHeight: dh}
	colour := node.Child(ebml.IDColour)
	if colour == nil {
		return v, nil
	}
	c := tracks.Colour{Present: true}
	if c.MatrixCoefficients, err = ebml.ReadUintChild(src, colour, ebml.IDMatrixCoefficients); err != nil {
		return nil, err
	}
	if c.TransferCharacterist, err = ebml.ReadUintChild(src, colour, ebml.IDTransferChars); err != nil {
		return nil, err
	}
	if c.Primaries, err = ebml.ReadUintChild(src, colour, ebml.IDPrimaries); err != nil {
		return nil, err
	}
	if c.Range, err = ebml.ReadUintChild(src, colour, ebml.IDRange); err != nil {
		return nil, err
	}
	if c.BitsPerChannel, err = ebml.ReadUintChild(src, colour, ebml.IDBitsPerChannel); err != nil {
		return nil, err
	}
	if c.ChromaSubsampleHorz, err = ebml.ReadUintChild(src, colour, ebml.IDChromaSubsampleHorz); err != nil {
		return nil, err
	}
	if c.ChromaSubsampleVert, err = ebml.ReadUintChild(src, colour, ebml.IDChromaSubsampleVert); err != nil {
		return nil, err
	}
	if n := colour.Child(ebml.IDMaxCLL); n != nil {
		c.HasMaxCLL = true
		if c.MaxCLL, err = ebml.ReadUint(src, n); err != nil {
			return nil, err
		}
	}
	if n := colour.Child(ebml.IDMaxFALL); n != nil {
		c.HasMaxFALL = true
		if c.MaxFALL, err = ebml.ReadUint(src, n); err != nil {
			return nil, err
		}
	}
	if n := colour.Child(ebml.IDMasteringMetadata); n != nil {
		c.MasteringPresent = true
		if _, err := ebml.ReadFloat(src, n.Child(ebml.IDLuminanceMax)); err != nil {
			return nil, err
		}
	}
	v.Colour = c
	return v, nil
}

// layoutFor maps the standard Matroska channel counts onto a named layout so
// the validator can compare layout and Channels without a manifest hint.
func layoutFor(channels uint64) tracks.ChannelLayout {
	switch channels {
	case 1:
		return tracks.LayoutMono
	case 2:
		return tracks.LayoutStereo
	case 3:
		return tracks.Layout2_1
	case 4:
		return tracks.LayoutQuad
	case 5:
		return tracks.Layout5_0
	case 6:
		return tracks.Layout5_1
	case 7:
		return tracks.Layout6_1
	case 8:
		return tracks.Layout7_1
	}
	return ""
}

func applyFragments(src io.ReadSeeker, tree *ebml.Tree, a *media.Asset) error {
	raw, err := ebml.CollectFragments(src, tree)
	if err != nil {
		return err
	}
	scale := a.TimecodeScale
	out := make([]media.Fragment, 0, len(raw))
	for i, f := range raw {
		startTicks := f.ClusterTime + f.Timecode
		if startTicks < 0 {
			return timeline.New(timeline.StageParse, timeline.CatIllegalLength, ebml.IDSimpleBlock,
				f.Offset, "block at %d resolves to negative time %d ticks", f.Offset, startTicks)
		}
		start := tickDuration(float64(startTicks), scale)
		var dur timeline.Nanosecond
		if tr := a.TrackByNumber(f.TrackNumber); tr != nil && tr.DefaultDuration > 0 {
			dur = timeline.Nanosecond(tr.DefaultDuration)
		}
		out = append(out, media.Fragment{
			TrackNumber: f.TrackNumber,
			Order:       i,
			Seq:         -1,
			Start:       start,
			Duration:    dur,
			Reference:   tickDuration(float64(f.ClusterTime), scale),
			Cluster:     f.ClusterIndex,
			Offset:      f.Offset,
			Keyframe:    true,
			Kind:        f.Kind,
		})
	}
	a.Fragments = out
	return nil
}

func recordElements(tree *ebml.Tree, a *media.Asset) {
	var walk func(*ebml.Node)
	walk = func(n *ebml.Node) {
		if n.ID != 0 {
			a.Elements = append(a.Elements, media.Element{
				ID: n.ID, Name: n.Name(), Offset: n.Offset, Size: uint64(n.End - n.Offset),
			})
			if n.Unknown {
				a.UnknownSized++
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(tree.Root)
	// Stable order: document order by offset, which is what a hex dump shows.
	sortElements(a.Elements)
}

func sortElements(es []media.Element) {
	for i := 1; i < len(es); i++ {
		for j := i; j > 0 && es[j].Offset < es[j-1].Offset; j-- {
			es[j], es[j-1] = es[j-1], es[j]
		}
	}
}

// Describe renders a one-line summary for diagnostics.
func Describe(a *media.Asset) string {
	return fmt.Sprintf("%s: %d elements, %d tracks, %d fragments",
		a.Path, a.ElementCount(), len(a.Tracks), len(a.Fragments))
}
