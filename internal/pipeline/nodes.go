package pipeline

import (
	"bytes"
	"os"
	"path/filepath"

	"regress/internal/fixture"
	"regress/internal/index"
	"regress/internal/manifest"
	"regress/internal/media"
	"regress/internal/mp4"
	"regress/internal/rtp"
	"regress/internal/subtitle"
)

// buildGraph constructs the eight-node graph. The returned offset is the exact
// corruption byte the fixture damaged, used to compute fault_offset_delta.
func (e *Engine) buildGraph() ([]*index.Node, int64) {
	a := e.assets
	// All nodes share one time line anchored at t=0 with a 7-second span.
	sharedBoundary := index.Boundary{Start: media.NewInt(0), End: media.NewInt(7)}
	srtBase := &index.Node{
		ID: nodeSRTBase, Kind: index.KindSubtitle,
		Input:    media.DigestBytes(media.KindPayload, a.SRTBase),
		Boundary: sharedBoundary,
		Compute:  e.computeSRT(a.SRTBase),
	}
	srtModified := &index.Node{
		ID: nodeSRTModified, Kind: index.KindSubtitle,
		Input:    media.DigestBytes(media.KindPayload, a.SRTModified),
		Boundary: sharedBoundary,
		Compute:  e.computeSRT(a.SRTModified),
	}
	vtt := &index.Node{
		ID: nodeVTT, Kind: index.KindSubtitle,
		Input:    media.DigestBytes(media.KindPayload, a.VTTBase),
		Boundary: sharedBoundary,
		Compute:  e.computeVTT(a.VTTBase),
	}
	rtpNode := &index.Node{
		ID: nodeRTP, Kind: index.KindRTP,
		Input:    rtpInputDigest(a),
		Boundary: sharedBoundary,
		Compute:  e.computeRTP(),
	}
	mp4H := &index.Node{
		ID: nodeMP4Healthy, Kind: index.KindMP4,
		Input:    media.DigestBytes(media.KindContainer, a.MP4Healthy),
		Boundary: sharedBoundary,
		Compute:  e.computeMP4(a.MP4Healthy, false),
	}
	mp4C := &index.Node{
		ID: nodeMP4Corrupt, Kind: index.KindMP4,
		Input:    media.DigestBytes(media.KindContainer, a.MP4Corrupt),
		Boundary: sharedBoundary,
		Compute:  e.computeMP4(a.MP4Corrupt, true),
	}
	// Merge node depends on all three subtitle sources; all share the t=0
	// origin so the boundary introduces no discontinuity.
	merge := &index.Node{
		ID: nodeSubtitle, Kind: index.KindSubtitle,
		Input:    "merge:" + srtBase.Input + "|" + srtModified.Input + "|" + vtt.Input,
		Deps:     []string{nodeSRTBase, nodeSRTModified, nodeVTT},
		Boundary: sharedBoundary,
		Compute: func(deps map[string]string) (string, []byte, error) {
			d := media.NewDigester(media.KindPayload)
			for _, k := range []string{nodeSRTBase, nodeSRTModified, nodeVTT} {
				d.WriteString(deps[k])
			}
			return d.Sum(), []byte("merged-subtitles"), nil
		},
	}
	// Manifest node depends on every upstream result.
	man := &index.Node{
		ID: nodeManifest, Kind: index.KindManifest,
		Input:    "manifest-input",
		Deps:     []string{nodeRTP, nodeMP4Healthy, nodeMP4Corrupt, nodeSubtitle},
		Boundary: sharedBoundary,
		Compute:  e.computeManifest(),
	}
	return []*index.Node{srtBase, srtModified, vtt, rtpNode, mp4H, mp4C, merge, man},
		a.CorruptOffset
}

func rtpInputDigest(a *fixture.Assets) string {
	d := media.NewDigester(media.KindContainer)
	for _, p := range a.RTPPackets {
		d.Write(p.Payload)
	}
	return d.Sum()
}

func (e *Engine) computeSRT(data []byte) func(map[string]string) (string, []byte, error) {
	return func(map[string]string) (string, []byte, error) {
		doc, err := subtitle.Parse(bytes.NewReader(data), "srt")
		if err != nil {
			return "", nil, err
		}
		return doc.PayloadDigest, []byte(doc.ContainerDigest), nil
	}
}

func (e *Engine) computeVTT(data []byte) func(map[string]string) (string, []byte, error) {
	return func(map[string]string) (string, []byte, error) {
		doc, err := subtitle.Parse(bytes.NewReader(data), "vtt")
		if err != nil {
			return "", nil, err
		}
		return doc.PayloadDigest, []byte(doc.ContainerDigest), nil
	}
}

func (e *Engine) computeRTP() func(map[string]string) (string, []byte, error) {
	return func(map[string]string) (string, []byte, error) {
		states, errs := rtp.Recover(e.assets.RTPPackets, map[uint32]uint32{0x5EED: 90000})
		if len(errs) > 0 {
			return "", nil, errs[0]
		}
		e.rtpState = states[0]
		return states[0].PayloadDigest, []byte(states[0].ContainerDigest), nil
	}
}

func (e *Engine) computeMP4(data []byte, corrupt bool) func(map[string]string) (string, []byte, error) {
	source := "asset-healthy.mp4"
	if corrupt {
		source = "asset-corrupt.mp4"
	}
	return func(map[string]string) (string, []byte, error) {
		fs, err := mp4.ScanReader(bytes.NewReader(data), source)
		if err != nil {
			return "", nil, err
		}
		// A corrupt track is an isolated, structured outcome carried in
		// fs.TrackErrors, not a node-level failure.
		return fs.ContainerDigest, mustJSON(fs), nil
	}
}

func (e *Engine) computeManifest() func(map[string]string) (string, []byte, error) {
	return func(deps map[string]string) (string, []byte, error) {
		variants := []manifest.Variant{
			{Name: "high", Bandwidth: 2000000, Codec: "avc1.640028", Subtitles: "sub",
				Segments: []manifest.Segment{
					{URI: "high-0.ts", Duration: media.NewRat(4, 1)},
					{URI: "high-1.ts", Duration: media.NewRat(3, 1)},
				}},
			{Name: "low", Bandwidth: 500000, Codec: "avc1.64001f", Subtitles: "sub",
				Segments: []manifest.Segment{
					{URI: "low-0.ts", Duration: media.NewRat(7, 1)},
				}},
		}
		pl := manifest.Build(variants)
		if err := e.writeManifest(pl); err != nil {
			return "", nil, err
		}
		return "manifest:" + pl.SHA256, []byte(pl.MasterText), nil
	}
}

func (e *Engine) writeManifest(pl *manifest.Playlist) error {
	path := filepath.Join(e.workDir, "out", "manifest.m3u8")
	return atomicWrite(path, []byte(pl.MasterText))
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
