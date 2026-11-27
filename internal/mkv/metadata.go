package mkv

import (
	"fmt"
	"sort"
	"strings"

	"tracecheck/internal/ebml"
)

// Metadata is the normalized, renderable summary of a parsed Matroska file.
// Every value carries whether it was present in the file or defaulted, and
// every duplicate that contradicts a kept value is listed so nothing is lost.
type Metadata struct {
	DocType        string
	TimestampScale int64
	ScaleDefaulted bool
	Tracks         []*Track
	Conflicts      []Conflict
	ClusterCount   int
	BlockCount     int
}

// BuildMetadata assembles the summary from a parsed document and its blocks.
func BuildMetadata(doc *ebml.Document, tracks *Tracks, blocks []Block) *Metadata {
	m := &Metadata{
		TimestampScale: tracks.TimestampScale,
		ScaleDefaulted: tracks.ScaleDefaulted,
		Tracks:         tracks.Items,
		ClusterCount:   len(doc.All(ebml.IDCluster)),
		BlockCount:     len(blocks),
	}
	if hdr := doc.Find(ebml.IDEBMLHeader); hdr != nil {
		for _, c := range doc.Children[hdr] {
			if c.ID == ebml.IDDocType {
				m.DocType = ebml.ReadString(doc.Payload(c))
				break
			}
		}
	}
	for _, tr := range tracks.Items {
		m.Conflicts = append(m.Conflicts, tr.Conflicts...)
	}
	sort.SliceStable(m.Conflicts, func(i, j int) bool {
		if m.Conflicts[i].Offset != m.Conflicts[j].Offset {
			return m.Conflicts[i].Offset < m.Conflicts[j].Offset
		}
		return m.Conflicts[i].Field < m.Conflicts[j].Field
	})
	return m
}

// Summary renders a stable, line-oriented description used in the demo output
// and stored alongside index checkpoints. Field order is fixed so two runs on
// identical input produce identical text.
func (m *Metadata) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "doctype=%s\n", orNone(m.DocType))
	if m.ScaleDefaulted {
		fmt.Fprintf(&b, "timestamp_scale=%d (default)\n", m.TimestampScale)
	} else {
		fmt.Fprintf(&b, "timestamp_scale=%d\n", m.TimestampScale)
	}
	fmt.Fprintf(&b, "clusters=%d blocks=%d tracks=%d\n", m.ClusterCount, m.BlockCount, len(m.Tracks))
	for _, tr := range m.Tracks {
		fmt.Fprintf(&b, "track %d type=%s codec=%s", tr.Number,
			orDefault(stringifyUint(tr.Type)), orNone(tr.CodecID.Value))
		switch tr.Type.Value {
		case TrackAudio:
			fmt.Fprintf(&b, " sr=%s ch=%s depth=%s",
				stringifyFloat(tr.Audio.SamplingFrequency),
				stringifyUint(tr.Audio.Channels),
				stringifyUint(tr.Audio.BitDepth))
			if tr.Audio.ChannelPositions.Present {
				fmt.Fprintf(&b, " channel_positions=%s", tr.Audio.ChannelPositions.Value)
			}
		case TrackVideo:
			fmt.Fprintf(&b, " px=%sx%s",
				stringifyUint(tr.Video.PixelWidth), stringifyUint(tr.Video.PixelHeight))
			fmt.Fprintf(&b, " matrix=%s range=%s transfer=%s primaries=%s",
				stringifyUint(tr.Video.Colour.MatrixCoefficients),
				stringifyUint(tr.Video.Colour.Range),
				stringifyUint(tr.Video.Colour.TransferCharacteristics),
				stringifyUint(tr.Video.Colour.Primaries))
		}
		b.WriteByte('\n')
	}
	for _, c := range m.Conflicts {
		fmt.Fprintf(&b, "conflict %s @%d kept=%s rejected=%s\n",
			c.Field, c.Offset, orNone(c.Kept), c.Rejected)
	}
	return b.String()
}

func stringifyUint(f Field[uint64]) string {
	if !f.Present {
		return "-"
	}
	if f.Defaulted {
		return fmt.Sprintf("%d(default)", f.Value)
	}
	return fmt.Sprintf("%d", f.Value)
}

func stringifyFloat(f Field[float64]) string {
	if !f.Present {
		return "-"
	}
	if f.Defaulted {
		return fmt.Sprintf("%g(default)", f.Value)
	}
	return fmt.Sprintf("%g", f.Value)
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func orDefault(s string) string {
	if s == "-" {
		return "default"
	}
	return s
}
