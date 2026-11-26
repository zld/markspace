package main

import (
	"encoding/binary"
	"fmt"
	"os"

	"continuity/internal/cue"
	"continuity/internal/fixture"
	"continuity/internal/manifest"
	"continuity/internal/media"
	"continuity/internal/resume"
)

type builtJob struct {
	data      []byte
	doc       *media.Document
	events    []media.Event
	index     *resume.TimelineIndex
	clusters  []resume.Cluster
	prefixSHA string
	inputs    []manifest.InputDigest
	hints     []cue.SwitchHint
}

func buildNormalJob(g *fixture.Generated, m *manifest.Manifest) (*builtJob, error) {
	data, err := media.ReadFileLimited(g.NormalMKV, media.DefaultLimits())
	if err != nil {
		return nil, err
	}
	doc, err := media.ParseEBML(data, media.DefaultLimits())
	if err != nil {
		return nil, err
	}
	blocks, err := media.ExtractBlocks(data, doc)
	if err != nil {
		return nil, err
	}

	trackMeta := map[uint32]manifest.TrackMeta{}
	kindFor := func(id uint32) media.EventKind {
		if t, ok := trackMeta[id]; ok {
			switch t.Type {
			case "video":
				return media.KindVideo
			case "audio":
				return media.KindAudio
			}
		}
		return media.KindCue
	}
	for _, t := range m.Tracks {
		trackMeta[t.ID] = t
	}
	for _, b := range blocks {
		if t, ok := trackMeta[b.TrackID]; ok && b.DurationNS == 0 {
			b.DurationNS = t.FrameDurationNS
		}
	}

	events, err := media.EventsFromBlocks(blocks, kindFor)
	if err != nil {
		return nil, err
	}

	mp4Data, err := os.ReadFile(g.EMSGMP4)
	if err != nil {
		return nil, err
	}
	hints, err := cue.ReadEMSG(mp4Data)
	if err != nil {
		return nil, err
	}
	for _, h := range hints {
		events = append(events, h.Event())
	}

	clusters := make([]resume.Cluster, 0)
	prefix := resume.NewPrefixHasher()
	for i, el := range doc.FindDescendants(media.IDCluster) {
		raw := append([]byte(nil), data[el.HeaderOffset:el.End]...)
		clusters = append(clusters, resume.Cluster{Index: i, Input: raw, Kind: "matroska-cluster"})
		_, _ = prefix.Write(raw)
	}

	index := resume.NewTimelineIndex(events)
	inputs := []manifest.InputDigest{}
	for _, path := range []string{g.NormalMKV, g.InterleavedMKV, g.UnknownLengthMKV, g.EMSGMP4, g.RTPWrap} {
		if path == "" {
			continue
		}
		st, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		sum, err := resume.SHA256File(path)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, manifest.InputDigest{Name: pathBase(path), Size: st.Size(), SHA256: sum})
	}

	return &builtJob{
		data:      data,
		doc:       doc,
		events:    events,
		index:     index,
		clusters:  clusters,
		prefixSHA: prefix.Sum(),
		inputs:    inputs,
		hints:     hints,
	}, nil
}

func pathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func readLengthPrefixedRTP(path string, track uint32) (*cue.RTPStream, []media.Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	stream := &cue.RTPStream{TrackID: track, Kind: media.KindVideo, ClockRate: 90000, SourceID: "rtp-backup"}
	var all []media.Event
	off := 0
	for off < len(data) {
		if off+4 > len(data) {
			return stream, all, fmt.Errorf("truncated RTP record length at %d", off)
		}
		n := int(binary.BigEndian.Uint32(data[off : off+4]))
		off += 4
		if n <= 0 || off+n > len(data) {
			return stream, all, fmt.Errorf("RTP record length %d out of bounds at %d", n, off-4)
		}
		events, err := stream.Feed(data[off:off+n], int64(off))
		if err != nil {
			return stream, all, err
		}
		all = append(all, events...)
		off += n
	}
	return stream, all, nil
}
