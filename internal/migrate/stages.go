package migrate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"example.com/assetcheck/internal/manifest"
	"example.com/assetcheck/internal/media"
	"example.com/assetcheck/internal/timeline"
	"example.com/assetcheck/internal/tracks"
)

// Stage names, in execution order. The CLI's --stop-after takes these values.
const (
	StageParse     = "parse"
	StageNormalize = "normalize"
	StageWrite     = "write"
)

// Stages lists the pipeline stages in order.
var Stages = []string{StageParse, StageNormalize, StageWrite}

// StageIndex returns the position of a stage name, or -1.
func StageIndex(name string) int {
	for i, s := range Stages {
		if s == name {
			return i
		}
	}
	return -1
}

// Options configure one migration run.
type Options struct {
	// Out is the output root; generations live beneath it.
	Out string
	// StopAfter ends the run successfully after the named stage, leaving a
	// resumable checkpoint set.
	StopAfter string
	// Resume continues from the last valid checkpoint.
	Resume bool
	// Manifest is the adapted manifest driving the migration.
	Manifest *manifest.Manifest
	// Asset is the parsed media. Only the parse stage reads it.
	Asset *media.Asset
}

// StageRecord is the outcome of one stage.
type StageRecord struct {
	Stage        string
	Index        int
	InputDigest  string
	OutputDigest string
	// ElapsedMS is duration in integer milliseconds, kept out of checkpoints so
	// resumed output stays byte-identical.
	ElapsedMS int64
	Skipped   bool // true when resume reused the recorded output
}

// Result summarises a run.
type Result struct {
	Records     []StageRecord
	FinalDigest string
	Generation  string
	Resumed     bool
	ResumeFrom  string
	RolledBack  bool
	Reason      string
}

// NormalizedManifest is the canonical, sorted output of the normalize stage.
// Its JSON encoding is stable: map keys are sorted by encoding/json and every
// slice is sorted before writing.
type NormalizedManifest struct {
	Version       int                        `json:"version"`
	AssetID       string                     `json:"asset_id,omitempty"`
	Title         string                     `json:"title,omitempty"`
	Languages     []string                   `json:"languages,omitempty"`
	MaxChannels   uint64                     `json:"max_channels,omitempty"`
	RequireColour bool                       `json:"require_colour,omitempty"`
	SequenceBase  int64                      `json:"sequence_base"`
	Tracks        []NormalizedTrack          `json:"tracks"`
	Audio         map[string]NormalizedAudio `json:"audio,omitempty"`
	Video         map[string]NormalizedVideo `json:"video,omitempty"`
	SourceVersion int                        `json:"source_version"`
	Fields        []string                   `json:"fields"`
}

// NormalizedTrack is a sorted track declaration.
type NormalizedTrack struct {
	Number   uint64 `json:"number"`
	Type     string `json:"type,omitempty"`
	CodecID  string `json:"codec_id,omitempty"`
	Language string `json:"language,omitempty"`
	Name     string `json:"name,omitempty"`
}

// NormalizedAudio is the canonical audio rule.
type NormalizedAudio struct {
	SampleRateHz manifest.RateRange `json:"sample_rate_hz"`
	Layout       string             `json:"channel_layout,omitempty"`
}

// NormalizedVideo is the canonical colour rule.
type NormalizedVideo struct {
	Colour manifest.ColourRule `json:"colour"`
}

// Run executes the migration. It is idempotent: the same inputs produce the
// same output digest whether or not the run was interrupted.
func Run(opts Options) (*Result, error) {
	if opts.Manifest == nil {
		return nil, timeline.New(timeline.StageManifest, timeline.CatManifestField, 0, -1,
			"migration requires a manifest")
	}
	if opts.Out == "" {
		return nil, timeline.New(timeline.StageCommit, timeline.CatIO, 0, -1,
			"migration requires an output root")
	}
	if opts.StopAfter != "" && StageIndex(opts.StopAfter) < 0 {
		return nil, timeline.New(timeline.StageCommit, timeline.CatIllegalLength, 0, -1,
			"unknown stage %q (want one of %v)", opts.StopAfter, Stages)
	}
	store, err := NewStore(opts.Out)
	if err != nil {
		return nil, err
	}
	manifestDigest := opts.Manifest.Digest()
	res := &Result{}
	existing, err := store.LoadAll()
	if err != nil {
		return nil, err
	}
	start := 0
	if opts.Resume {
		start, res, err = resumePoint(store, existing, opts, manifestDigest)
		if err != nil {
			return nil, err
		}
	} else if len(existing) > 0 {
		// A fresh run over a root with stale checkpoints starts clean.
		if err := store.Clear(-1); err != nil {
			return nil, err
		}
	}

	normalized, err := buildNormalized(opts.Manifest)
	if err != nil {
		return nil, err
	}
	manifestOut, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return nil, timeline.Wrap(err, timeline.StageNormalize, timeline.CatIO, 0, -1,
			"encode normalized manifest")
	}
	manifestOut = append(manifestOut, '\n')

	gen, err := generationName(manifestDigest, opts.Asset, opts.Manifest)
	if err != nil {
		return nil, err
	}
	genDir := filepath.Join(opts.Out, gen)

	if opts.Resume && start > 0 {
		// Report the checkpoints resume reused so the CLI can show that the
		// completed stages were verified rather than rerun.
		for _, cp := range existing {
			if cp.Index >= start {
				continue
			}
			res.Records = append(res.Records, StageRecord{
				Stage: cp.Stage, Index: cp.Index, InputDigest: cp.InputDigest,
				OutputDigest: cp.OutputDigest, Skipped: true,
			})
		}
		sort.Slice(res.Records, func(i, j int) bool { return res.Records[i].Index < res.Records[j].Index })
	}
	for i := start; i < len(Stages); i++ {
		stage := Stages[i]
		rec, err := runStage(stage, i, store, genDir, opts, manifestDigest, manifestOut, existing)
		if err != nil {
			return nil, err
		}
		res.Records = append(res.Records, rec)
		if opts.StopAfter != "" && stage == opts.StopAfter {
			break
		}
	}
	earlyStop := opts.StopAfter != "" && StageIndex(opts.StopAfter) < len(Stages)-1
	if earlyStop {
		// Stopped early: checkpoints exist, nothing is published.
		res.Generation = gen
		return res, nil
	}
	if start >= len(Stages) {
		// Resume found every stage checkpointed. Publish if the previous run
		// stopped after write but before commit.
		cur, err := store.ReadCurrent()
		if err != nil {
			return nil, err
		}
		if cur.Generation != "" {
			digest, err := GenerationDigest(filepath.Join(opts.Out, cur.Generation))
			if err != nil {
				return nil, err
			}
			res.Generation = cur.Generation
			res.FinalDigest = digest
			return res, nil
		}
	}
	// Publish the staged output atomically.
	digest, err := commitGeneration(opts.Out, genDir)
	if err != nil {
		return nil, err
	}
	if err := store.CommitGeneration(Current{Generation: gen, Digest: digest}); err != nil {
		return nil, err
	}
	res.Generation = gen
	res.FinalDigest = digest
	return res, nil
}

// resumePoint loads and verifies checkpoints, returning the first stage that
// still needs work.
func resumePoint(store *Store, existing []Checkpoint, opts Options, manifestDigest string) (int, *Result, error) {
	res := &Result{Resumed: true}
	if len(existing) == 0 {
		res.Reason = "no checkpoint found; starting from parse"
		return 0, res, nil
	}
	if opts.Asset == nil {
		return 0, nil, timeline.New(timeline.StageResume, timeline.CatCheckpointState, 0, -1,
			"resume requires the same input asset")
	}
	inputDigest := opts.Asset.SHA256
	for _, cp := range existing {
		if cp.InputDigest != "" && cp.InputDigest != inputDigest {
			keep := cp.Index - 1
			if err := store.Clear(keep); err != nil {
				return 0, nil, err
			}
			res.RolledBack = true
			res.ResumeFrom = cp.Stage
			res.Reason = fmt.Sprintf("input digest %s does not match checkpoint %s digest %s; rolled back to stage %d",
				inputDigest, cp.Stage, cp.InputDigest, keep)
			return 0, res, nil
		}
		if cp.ManifestDigest != "" && cp.ManifestDigest != manifestDigest {
			keep := cp.Index - 1
			if err := store.Clear(keep); err != nil {
				return 0, nil, err
			}
			res.RolledBack = true
			res.ResumeFrom = cp.Stage
			res.Reason = fmt.Sprintf("manifest digest %s does not match checkpoint %s digest %s; rolled back to stage %d",
				manifestDigest, cp.Stage, cp.ManifestDigest, keep)
			return 0, res, nil
		}
		if cp.ManifestVersion != 0 && opts.Manifest != nil && cp.ManifestVersion != opts.Manifest.Version {
			keep := cp.Index - 1
			if err := store.Clear(keep); err != nil {
				return 0, nil, err
			}
			res.RolledBack = true
			res.ResumeFrom = cp.Stage
			res.Reason = fmt.Sprintf("manifest version %d does not match checkpoint version %d; rolled back to stage %d",
				opts.Manifest.Version, cp.ManifestVersion, keep)
			return 0, res, nil
		}
		if cp.Failure != nil {
			res.ResumeFrom = cp.Stage
			res.Reason = fmt.Sprintf("checkpoint %s recorded failure %s at offset %d; retrying it",
				cp.Stage, cp.Failure.Category, cp.Failure.Offset)
			return cp.Index, res, nil
		}
	}
	// Every recorded stage is valid; continue after the last one.
	last := existing[len(existing)-1]
	if last.Index >= len(Stages)-1 {
		res.Reason = "all stages already complete; verifying committed output"
		res.FinalDigest = last.OutputDigest
		return len(Stages), res, nil
	}
	res.ResumeFrom = Stages[last.Index+1]
	res.Reason = fmt.Sprintf("resuming at %s after verified checkpoint %s", Stages[last.Index+1], last.Stage)
	return last.Index + 1, res, nil
}

func buildNormalized(m *manifest.Manifest) (*NormalizedManifest, error) {
	n := &NormalizedManifest{
		Version:       m.Version,
		AssetID:       m.AssetID,
		Title:         m.Title,
		Languages:     append([]string(nil), m.Languages...),
		MaxChannels:   m.MaxChannels,
		RequireColour: m.RequireColour,
		SequenceBase:  m.SequenceBase,
		SourceVersion: m.Version,
		Fields:        m.FieldPaths(),
	}
	sort.Strings(n.Languages)
	for _, t := range m.Tracks {
		n.Tracks = append(n.Tracks, NormalizedTrack{
			Number: t.Number, Type: t.Type, CodecID: t.CodecID, Language: t.Lang, Name: t.Name,
		})
	}
	sort.Slice(n.Tracks, func(i, j int) bool { return n.Tracks[i].Number < n.Tracks[j].Number })
	if len(m.AudioRates) > 0 || len(m.Layouts) > 0 {
		n.Audio = map[string]NormalizedAudio{}
		for num, rate := range m.AudioRates {
			key := fmt.Sprintf("%d", num)
			entry := n.Audio[key]
			entry.SampleRateHz = rate
			n.Audio[key] = entry
		}
		for num, layout := range m.Layouts {
			key := fmt.Sprintf("%d", num)
			entry := n.Audio[key]
			entry.Layout = layout
			n.Audio[key] = entry
		}
	}
	if len(m.VideoColour) > 0 {
		n.Video = map[string]NormalizedVideo{}
		for num, rule := range m.VideoColour {
			n.Video[fmt.Sprintf("%d", num)] = NormalizedVideo{Colour: rule}
		}
	}
	return n, nil
}

// generationName derives a deterministic directory name from the inputs.
func generationName(manifestDigest string, asset *media.Asset, m *manifest.Manifest) (string, error) {
	input := ""
	if asset != nil {
		input = asset.SHA256
	}
	seed := input + "\x00" + manifestDigest + "\x00" + fmt.Sprintf("%d", m.Version)
	return "gen-" + Digest([]byte(seed))[:16], nil
}

// runStage executes one stage, reusing a valid completed checkpoint on resume.
func runStage(stage string, index int, store *Store, genDir string, opts Options,
	manifestDigest string, manifestOut []byte, existing []Checkpoint) (StageRecord, error) {
	for _, cp := range existing {
		if cp.Index == index && cp.Stage == stage && cp.Failure == nil &&
			cp.InputDigest == digestInput(opts) && cp.ManifestDigest == manifestDigest {
			if stage == StageWrite {
				if _, err := os.Stat(writtenPath(genDir)); err == nil {
					return StageRecord{
						Stage: stage, Index: index, InputDigest: cp.InputDigest,
						OutputDigest: cp.OutputDigest, Skipped: true,
					}, nil
				}
			} else {
				return StageRecord{
					Stage: stage, Index: index, InputDigest: cp.InputDigest,
					OutputDigest: cp.OutputDigest, Skipped: true,
				}, nil
			}
		}
	}

	cp := Checkpoint{
		Stage: stage, Index: index, InputDigest: digestInput(opts),
		ManifestDigest: manifestDigest, ManifestVersion: opts.Manifest.Version,
	}
	rec := StageRecord{Stage: stage, Index: index, InputDigest: cp.InputDigest}
	switch stage {
	case StageParse:
		if opts.Asset == nil {
			return rec, timeline.New(timeline.StageParse, timeline.CatIO, 0, -1,
				"parse stage requires an input asset")
		}
		summary := parseSummary(opts.Asset)
		cp.OutputDigest = Digest(summary)
	case StageNormalize:
		cp.OutputDigest = Digest(manifestOut)
	case StageWrite:
		if err := writeGeneration(genDir, manifestOut); err != nil {
			cp.Failure = failureFrom(err, stage)
			if serr := store.Save(cp); serr != nil {
				return rec, serr
			}
			return rec, err
		}
		cp.OutputDigest = Digest(manifestOut)
	default:
		return rec, timeline.New(timeline.StageCommit, timeline.CatIllegalLength, 0, -1,
			"unknown stage %q", stage)
	}
	rec.OutputDigest = cp.OutputDigest
	if err := store.Save(cp); err != nil {
		return rec, err
	}
	return rec, nil
}

func digestInput(opts Options) string {
	if opts.Asset != nil {
		return opts.Asset.SHA256
	}
	return ""
}

// parseSummary is the canonical, deterministic projection of a parsed asset that
// the parse stage digests. Only stable fields are included.
func parseSummary(a *media.Asset) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "container=%s\ndoctype=%s\nversion=%d\nsize=%d\nsha256=%s\n",
		a.Container, a.DocType, a.Version, a.Size, a.SHA256)
	tracks := append([]tracks.Track(nil), a.Tracks...)
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Number < tracks[j].Number })
	for _, t := range tracks {
		fmt.Fprintf(&b, "track=%d type=%s codec=%s lang=%s name=%s offset=%d\n",
			t.Number, t.Type.Name(), t.CodecID, t.Language, t.Name, t.Offset)
	}
	for _, f := range a.Fragments {
		fmt.Fprintf(&b, "frag track=%d order=%d start=%d dur=%d offset=%d\n",
			f.TrackNumber, f.Order, int64(f.Start), int64(f.Duration), f.Offset)
	}
	return []byte(b.String())
}

func failureFrom(err error, stage string) *Failure {
	f := &Failure{Stage: stage, Offset: -1, Category: string(timeline.CategoryOf(err))}
	var te *timeline.Error
	if asError(err, &te) {
		f.Offset = te.Offset
		f.ElementID = te.ElementID
		f.Reason = te.Reason
		if te.Category == "" {
			f.Category = string(timeline.CatIO)
		}
	} else {
		f.Reason = err.Error()
	}
	return f
}

func asError(err error, target **timeline.Error) bool {
	for err != nil {
		if e, ok := err.(*timeline.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// writeGeneration writes every output file for a generation into a staging
// directory, then renames the staging directory into place. A failure leaves the
// previous generation untouched.
func writeGeneration(genDir string, manifestOut []byte) error {
	parent := filepath.Dir(genDir)
	staging, err := os.MkdirTemp(parent, ".staging-"+filepath.Base(genDir)+"-")
	if err != nil {
		return timeline.Wrap(err, timeline.StageWrite, timeline.CatIO, 0, -1,
			"create staging dir")
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(staging)
		}
	}()
	// Commit order matters: write the payloads first and the completion marker
	// last, so a truncated run is detectable by the missing marker.
	if err := writeFile(filepath.Join(staging, "manifest.json"), manifestOut); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(staging, "index.json"), indexJSON()); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(staging, completionMarker), []byte("ok\n")); err != nil {
		return err
	}
	if err := os.Rename(staging, genDir); err != nil {
		return timeline.Wrap(err, timeline.StageCommit, timeline.CatIO, 0, -1,
			"rename staging generation into place")
	}
	ok = true
	return nil
}

const completionMarker = ".complete"

func writtenPath(genDir string) string { return filepath.Join(genDir, completionMarker) }

func indexJSON() []byte {
	return []byte("{\n  \"files\": [\"index.json\", \"manifest.json\"],\n  \"format\": \"assetcheck/v1\"\n}\n")
}

func writeFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return timeline.Wrap(err, timeline.StageWrite, timeline.CatIO, 0, -1,
			"write %s", filepath.Base(path))
	}
	return nil
}

// commitGeneration verifies a fully written generation and returns the SHA-256
// over its file contents in a fixed name order.
func commitGeneration(out, genDir string) (string, error) {
	if _, err := os.Stat(writtenPath(genDir)); err != nil {
		return "", timeline.Wrap(err, timeline.StageCommit, timeline.CatCheckpointState, 0, -1,
			"generation %s is missing its completion marker", filepath.Base(genDir))
	}
	return GenerationDigest(genDir)
}

// GenerationDigest hashes the manifest and index bytes of a generation in sorted
// name order, so the same inputs always yield the same digest.
func GenerationDigest(genDir string) (string, error) {
	names := []string{"index.json", "manifest.json"}
	sort.Strings(names)
	var all []byte
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(genDir, name))
		if err != nil {
			return "", timeline.Wrap(err, timeline.StageCommit, timeline.CatIO, 0, -1,
				"read %s for digest", name)
		}
		all = append(all, []byte(name+"\n")...)
		all = append(all, data...)
	}
	return Digest(all), nil
}

// ReadCommitted returns the manifest bytes of the currently committed
// generation, if any.
func ReadCommitted(store *Store) ([]byte, Current, error) {
	cur, err := store.ReadCurrent()
	if err != nil {
		return nil, Current{}, err
	}
	if cur.Generation == "" {
		return nil, cur, nil
	}
	data, err := os.ReadFile(filepath.Join(store.Root, cur.Generation, "manifest.json"))
	if err != nil {
		return nil, cur, timeline.Wrap(err, timeline.StageResume, timeline.CatIO, 0, -1,
			"read committed manifest")
	}
	return data, cur, nil
}

// Copy is a small helper used by tests to feed one reader twice.
func Copy(dst io.Writer, src io.Reader) error {
	_, err := io.Copy(dst, src)
	return err
}
