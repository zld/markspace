package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"archive-lab/internal/manifest"
	"archive-lab/internal/manifest/scan"
	"archive-lab/internal/media"
	"archive-lab/internal/quality"
	"github.com/pion/rtp"
)

// Options configures one restoration run. All paths are absolute or relative
// to the process working directory; every segment path must resolve inside
// InputRoot.
type Options struct {
	InputPath   string
	InputRoot   string
	IndexPath   string
	CacheDir    string
	ArtifactDir string
	Metadata    []string
	Request     quality.Request
	Limits      scan.Limits
	// ProgressPath persists completed segment indices for resume.
	ProgressPath string
	// DryRun stops after planning: the report shows the chosen configuration
	// and constraints without reading any segment payload.
	DryRun bool
}

// SegmentOutcome records what happened to one segment, so a failed or
// cancelled run can resume at the first segment without an outcome.
type SegmentOutcome struct {
	Index    int     `json:"index"`
	Rel      string  `json:"rel"`
	SHA256   string  `json:"sha256"`
	Bytes    int64   `json:"bytes"`
	OutBytes int     `json:"out_bytes"`
	PSNR     float64 `json:"psnr,omitempty"`
	PSNRSet  bool    `json:"psnr_set,omitempty"`
}

// Result is the internal outcome of a run; the CLI renders it as a Report.
type Result struct {
	Report       *Report
	Outcomes     []SegmentOutcome
	CacheState   CacheState
	IndexRebuilt bool
	IndexDigest  string
	Error        error
}

// CancelFunc is checked between segments; returning true stops the run at a
// segment boundary and records progress up to (not including) that segment.
type CancelFunc func() bool

// Runner executes the pipeline. It is stateless apart from the cache handle.
type Runner struct {
	Cache *Cache
}

// RanOptions are the resolved inputs after parsing and scanning.
type Prepared struct {
	Playlist    *manifest.Playlist
	ManifestRaw []byte
	Index       *scan.Index
	Rebuilt     bool
	CacheState  CacheState
	CacheKey    string
	CacheEntry  *Entry
	Sample      quality.Sample
	// ReplacedDigest is the previous input digest when an index existed but
	// no longer matches the live input; its cache entries are stale.
	ReplacedDigest string
}

// Prepare parses the manifest, reuses or rebuilds the index, and resolves the
// cache key. A corrupt or missing index falls back to a full scan; a content
// change at the same path is detected by digest and forces a rebuild.
func (r *Runner) Prepare(opts Options) (*Prepared, error) {
	raw, err := os.ReadFile(opts.InputPath)
	if err != nil {
		return nil, media.Wrap(media.StageScan, "input", err, "cannot read manifest").WithPath(opts.InputPath)
	}
	pl, err := ParseManifest(opts.InputPath, raw)
	if err != nil {
		return nil, err
	}
	// A master playlist names renditions; processing continues on the
	// selected variant's media playlist, whose own relative paths are still
	// resolved against the input root.
	if pl.Kind == manifest.KindMaster {
		variant, verr := selectVariant(pl)
		if verr != nil {
			return nil, verr
		}
		rel, rerr := scan.Resolve(rootOf(opts), variant.URI)
		if rerr != nil {
			return nil, rerr
		}
		full := filepath.Join(rootOf(opts), filepath.FromSlash(rel))
		raw, err = os.ReadFile(full)
		if err != nil {
			return nil, media.Wrap(media.StageScan, "input", err, "cannot read variant playlist").WithPath(variant.URI)
		}
		pl, err = ParseManifest(full, raw)
		if err != nil {
			return nil, err
		}
		if pl.Kind != manifest.KindMedia {
			return nil, media.New(media.StageParse, "hls", "variant %q is not a media playlist", variant.URI).WithPath(variant.URI)
		}
	}
	p := &Prepared{Playlist: pl, ManifestRaw: raw}
	// The effective manifest is the resolved media playlist: index entries
	// and digests describe the file whose segments are actually processed.
	manifestPath := opts.InputPath
	if pl.Path != "" && filepath.Clean(pl.Path) != filepath.Clean(opts.InputPath) {
		manifestPath = pl.Path
	}
	root := rootOf(opts)

	// Every media file must be structurally valid before any index is
	// committed: damaged input is a hard error, never a partial index.
	if err := validateContainers(root, pl); err != nil {
		return nil, err
	}

	// Try the saved index first. It is trusted only when both its own digest
	// and the live file digests still match.
	var old *scan.Index
	if opts.IndexPath != "" {
		idx, lerr := scan.Load(opts.IndexPath)
		switch {
		case lerr == nil:
			if !indexMatchesLive(idx, root, pl) {
				p.Rebuilt = true
			} else {
				old = idx
			}
		case lerr == scan.ErrIndexMissing:
			p.Rebuilt = true
		default:
			// Corrupt index: recompute rather than trust anything from it.
			p.Rebuilt = true
		}
	}
	if old != nil {
		// Re-verify the manifest bytes against the index even when every
		// file digest matches, so a changed manifest alone triggers rebuild.
		if old.Manifest.SHA256 != scan.HashBytes(raw) {
			p.Rebuilt = true
			p.ReplacedDigest = old.InputDigest()
		} else {
			p.Index = old
		}
	}
	if p.Index == nil {
		idx, serr := scan.Scan(scan.Input{
			ManifestPath:   manifestPath,
			Playlist:       pl,
			MediaRoot:      root,
			PlaylistDigest: scan.HashBytes(raw),
			Metadata:       opts.Metadata,
			Limits:         opts.Limits,
		})
		if serr != nil {
			return nil, serr
		}
		p.Index = idx
		p.Rebuilt = true
		if opts.IndexPath != "" {
			if err := idx.Save(opts.IndexPath); err != nil {
				return nil, err
			}
		}
	} else if opts.IndexPath != "" {
		// Index was valid and reused; nothing to write.
	}
	return p, nil
}

// validateContainers walks every media and init file with the MP4 probe so a
// truncated or otherwise damaged segment is reported with its path and
// offset before any index is written.
// rootOf resolves the input root: the explicit setting, or the manifest's
// directory.
func rootOf(opts Options) string {
	if opts.InputRoot != "" {
		return opts.InputRoot
	}
	return scan.RootOf(opts.InputPath)
}

// selectVariant picks the highest-bandwidth variant, the standard default.
func selectVariant(pl *manifest.Playlist) (manifest.Variant, error) {
	var best manifest.Variant
	for _, v := range pl.Variants {
		if v.Bandwidth > best.Bandwidth {
			best = v
		}
	}
	if best.URI == "" {
		return manifest.Variant{}, media.New(media.StageParse, "hls", "master playlist has no usable variant").WithPath(pl.Path)
	}
	return best, nil
}

func validateContainers(root string, pl *manifest.Playlist) error {
	refs := scan.SegmentRefs(pl)
	seen := make(map[string]bool)
	check := func(uri string) error {
		rel, err := scan.Resolve(root, uri)
		if err != nil {
			return err
		}
		if seen[rel] {
			return nil
		}
		seen[rel] = true
		full := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := media.ProbeFile(full, media.ProbeOptions{}); err != nil {
			return err
		}
		return nil
	}
	for _, ref := range refs {
		if err := check(ref.Rel); err != nil {
			return err
		}
	}
	for _, rep := range pl.Representations {
		if rep.InitURI == "" {
			continue
		}
		if err := check(rep.InitURI); err != nil {
			return err
		}
	}
	return nil
}

// indexMatchesLive checks every recorded file against its live digest without
// hashing anything not in the index.
func indexMatchesLive(idx *scan.Index, root string, pl *manifest.Playlist) bool {
	refs := scan.SegmentRefs(pl)
	recorded := 0
	for _, f := range idx.Files {
		if f.Kind != scan.KindMedia && f.Kind != scan.KindInit {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(f.Path))
		d, err := scan.HashFile(full, 0)
		if err != nil || d.SHA256 != f.SHA256 || d.Bytes != f.Bytes {
			return false
		}
		if _, perr := media.ProbeFile(full, media.ProbeOptions{}); perr != nil {
			return false
		}
		recorded++
	}
	// Every live segment must be represented in the index; a manifest that
	// gained a segment invalidates the whole index.
	if recorded < len(refs) && len(pl.Representations) == 0 {
		return false
	}
	for _, t := range idx.Files {
		if t.Kind == scan.KindMetadata {
			full := filepath.Join(root, filepath.FromSlash(t.Path))
			d, err := scan.HashFile(full, 0)
			if err != nil || d.SHA256 != t.SHA256 {
				return false
			}
		}
	}
	return true
}

// ParseManifest dispatches on content, not file extension, so fixtures can be
// named freely while malformed inputs still produce located errors.
func ParseManifest(path string, raw []byte) (*manifest.Playlist, error) {
	trimmed := raw
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\n' || trimmed[0] == '\r' || trimmed[0] == '\t') {
		trimmed = trimmed[1:]
	}
	if len(trimmed) > 0 && (trimmed[0] == '<' || trimmed[0] == 0xEF) {
		return manifest.ParseDASH(path, raw, manifest.Limits{})
	}
	return manifest.ParseHLS(path, raw, manifest.Limits{})
}

// segmentRefsFor returns the segment list of the selected representation
// (first representation for DASH, the media playlist otherwise).
func segmentRefsFor(pl *manifest.Playlist) ([]manifest.Segment, error) {
	switch pl.Kind {
	case manifest.KindMedia:
		return pl.Segments, nil
	case manifest.KindDASH:
		if len(pl.Representations) == 0 {
			return nil, media.New(media.StagePlan, "select", "MPD has no representation")
		}
		return pl.Representations[0].Segments, nil
	default:
		return nil, media.New(media.StagePlan, "select", "master playlist must be resolved before segment listing")
	}
}

// streamSegments reads each adopted segment exactly once, verifies its length
// and content digest, tracks timing and produces per-segment outcomes.
func (r *Runner) streamSegments(opts Options, prep *Prepared, segs []manifest.Segment, adopted []int, cfg quality.Config, cancel CancelFunc) ([]SegmentOutcome, int64, error) {
	root := opts.InputRoot
	if root == "" {
		root = scan.RootOf(opts.InputPath)
	}
	sem := make(chan struct{}, 2)
	outcomes := make([]SegmentOutcome, 0, len(adopted))
	var inBytes int64
	for _, i := range adopted {
		if cancel != nil && cancel() {
			return outcomes, inBytes, media.New(media.StageExecute, "stream", "cancelled before segment %d", i).WithSegment(i)
		}
		seg := segs[i]
		rel, err := scan.Resolve(root, seg.URI)
		if err != nil {
			return outcomes, inBytes, media.Wrap(media.StageExecute, "stream", err, "segment %d", i).WithSegment(i).WithPath(seg.URI)
		}
		entry, ok := prep.Index.SegmentDigest(rel, seg.URI)
		if !ok {
			return outcomes, inBytes, media.New(media.StageExecute, "stream", "segment %d is not covered by the index", i).
				WithPath(seg.URI).WithSegment(i)
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		oc, err := r.streamOne(full, seg.URI, entry, seg, cfg, sem)
		if err != nil {
			return outcomes, inBytes, err
		}
		inBytes += oc.Bytes
		outcomes = append(outcomes, oc)
	}
	return outcomes, inBytes, nil
}

func (r *Runner) streamOne(full, uri string, entry scan.FileEntry, seg manifest.Segment, cfg quality.Config, sem chan struct{}) (SegmentOutcome, error) {
	sem <- struct{}{}
	defer func() { <-sem }()
	f, err := os.Open(full)
	if err != nil {
		return SegmentOutcome{}, media.Wrap(media.StageExecute, "stream", err, "cannot open segment").
			WithPath(uri).WithSegment(seg.Index)
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64*1024)
	var n int64
	for {
		m, rerr := f.Read(buf)
		if m > 0 {
			h.Write(buf[:m])
			n += int64(m)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return SegmentOutcome{}, media.Wrap(media.StageExecute, "stream", rerr, "read failed at offset %d", n).
				WithPath(uri).WithOffset(n).WithSegment(seg.Index)
		}
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != entry.SHA256 {
		return SegmentOutcome{}, media.New(media.StageExecute, "stream", "segment content changed during run (digest %s, index %s)", got, entry.SHA256).
			WithPath(uri).WithSegment(seg.Index)
	}
	if n != entry.Bytes {
		return SegmentOutcome{}, media.New(media.StageExecute, "stream", "segment is %d bytes, index recorded %d", n, entry.Bytes).
			WithPath(uri).WithSegment(seg.Index)
	}
	if err := media.CheckDrift(seg.DurationNs, seg.DurationNs, "segment", seg.Index); err != nil {
		return SegmentOutcome{}, err
	}
	return SegmentOutcome{Index: seg.Index, Rel: uri, SHA256: got, Bytes: n}, nil
}

// RTPProbe summarises a packet stream and checks sequence continuity. The
// stream uses RFC 4571 framing (2-byte big-endian length per packet), the
// standard way to carry RTP over a byte-oriented transport. It never invents
// timestamps for non-RTP inputs.
type RTPProbe struct {
	Packets    int
	Bytes      int
	FirstTS    uint32
	LastTS     uint32
	FirstSeq   uint16
	LastSeq    uint16
	Disordered int
	Lost       int
}

// ProbeRTP parses an RFC 4571 framed RTP stream.
func ProbeRTP(data []byte) (*RTPProbe, error) {
	p := &RTPProbe{}
	off := 0
	for off < len(data) {
		if len(data)-off < 2 {
			return nil, media.New(media.StageExecute, "rtp", "truncated frame length at offset %d", off).
				WithOffset(int64(off))
		}
		n := int(data[off])<<8 | int(data[off+1])
		off += 2
		if n == 0 || len(data)-off < n {
			return nil, media.New(media.StageExecute, "rtp", "frame at offset %d declares %d bytes, %d available", off-2, n, len(data)-off).
				WithOffset(int64(off - 2))
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(data[off : off+n]); err != nil {
			return nil, media.New(media.StageExecute, "rtp", "invalid packet at offset %d: %v", off, err).
				WithOffset(int64(off))
		}
		if p.Packets == 0 {
			p.FirstTS, p.FirstSeq = pkt.Timestamp, pkt.SequenceNumber
			p.LastTS, p.LastSeq = pkt.Timestamp, pkt.SequenceNumber
		} else {
			want := p.LastSeq + 1
			if pkt.SequenceNumber != want {
				p.Disordered++
				if int16(pkt.SequenceNumber-want) > 0 {
					p.Lost += int(pkt.SequenceNumber - want)
				}
			}
			p.LastTS, p.LastSeq = pkt.Timestamp, pkt.SequenceNumber
		}
		p.Packets++
		p.Bytes += n
		off += n
	}
	return p, nil
}

// SegmentSample reads the media of the first adopted segment, probes its
// container and returns decoded luma frames when the codec is uncompressed.
// A container that cannot be parsed is a hard, located error: damaged input
// is never silently skipped.
func SegmentSample(fullPath, uri string, maxLumaBytes int64) (quality.Sample, *media.FileProbe, error) {
	probe, err := media.ProbeFile(fullPath, media.ProbeOptions{ExtractLuma: true, MaxLumaBytes: maxLumaBytes})
	if err != nil {
		var me *media.Error
		if errorsAs(err, &me) {
			return quality.Sample{}, nil, me.WithPath(uri)
		}
		return quality.Sample{}, nil, err
	}
	sample := quality.Sample{}
	if v := probe.Video(); v != nil && v.EntryType == "raw " && len(probe.Luma) > 0 {
		fpsNum, fpsDen := frameRate(v, probe)
		sample.Spec = quality.VideoSpec{Width: v.Width, Height: v.Height, FPSNum: fpsNum, FPSDen: fpsDen}
		sample.Frames = probe.Luma
	}
	return sample, probe, nil
}

// frameRate derives an exact rational frame rate from the sample table, or
// from fragment durations when the file is fragmented.
func frameRate(t *media.Track, probe *media.FileProbe) (int64, int64) {
	if t.Timescale == 0 {
		return 0, 0
	}
	var count, ticks uint64
	if len(t.SttsEntries) > 0 {
		for _, e := range t.SttsEntries {
			count += uint64(e.SampleCount)
			ticks += uint64(e.SampleCount) * uint64(e.SampleDelta)
		}
	} else {
		for _, f := range probe.Fragments {
			count += uint64(len(f.SampleOffsets))
			ticks += f.DurationTicks
		}
	}
	if count == 0 || ticks == 0 {
		return 0, 0
	}
	// fps = count * timescale / ticks
	num := int64(count) * int64(t.Timescale)
	den := int64(ticks)
	g := gcd(num, den)
	return num / g, den / g
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

func errorsAs(err error, target **media.Error) bool { return errors.As(err, target) }
