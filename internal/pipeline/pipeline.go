package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"media-audit/internal/cache"
	"media-audit/internal/index"
	"media-audit/internal/manifest"
	"media-audit/internal/media"
	"media-audit/internal/mp4"
	"media-audit/internal/mxf"
	"media-audit/internal/source"
	"media-audit/internal/task"
)

type Pipeline struct {
	dir     string
	tasks   *task.Store
	index   *index.Store
	cache   *cache.Store
	tracker *source.Tracker
}

type parsedInput struct {
	Path string
	Data []byte
	Doc  *media.Document
}

func New(stateDir string) (*Pipeline, error) {
	tasks, err := task.NewStore(stateDir)
	if err != nil {
		return nil, err
	}
	idx, err := index.Open(filepath.Join(stateDir, "index.json"))
	if err != nil {
		return nil, err
	}
	return &Pipeline{
		dir:     stateDir,
		tasks:   tasks,
		index:   idx,
		cache:   cache.NewStore(),
		tracker: source.NewTracker(),
	}, nil
}

func (p *Pipeline) Close() error { return p.index.Save() }

func readInputs(paths []string) ([]*parsedInput, error) {
	out := make([]*parsedInput, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		out = append(out, &parsedInput{Path: path, Data: data})
	}
	return out, nil
}

func parseInputs(inputs []*parsedInput) {
	for _, in := range inputs {
		switch strings.ToLower(filepath.Ext(in.Path)) {
		case ".mxf":
			in.Doc = mxf.Parse(in.Path, in.Data)
		case ".mp4":
			in.Doc = mp4.Parse(in.Path, in.Data)
		default:
			in.Doc = &media.Document{Format: media.Format("UNKNOWN"), Path: in.Path, Size: int64(len(in.Data))}
		}
	}
}

func readAndParse(paths []string) ([]*parsedInput, error) {
	out, err := readInputs(paths)
	if err != nil {
		return nil, err
	}
	parseInputs(out)
	return out, nil
}

func bundleFingerprint(key string, in []*parsedInput, metaVersion string) source.Fingerprint {
	h := sha256.New()
	structH := sha256.New()
	for _, x := range in {
		h.Write(x.Data)
		if x.Doc != nil {
			structH.Write([]byte(x.Doc.StructureSHA256))
			structH.Write([]byte{0})
		}
	}
	return source.Fingerprint{
		Path:            key,
		Size:            int64(len(in)),
		ContentSHA256:   hex.EncodeToString(h.Sum(nil)),
		StructureSHA256: hex.EncodeToString(structH.Sum(nil)),
		MetadataVersion: metaVersion,
	}
}

func (p *Pipeline) cachedDone(plan task.ResumePlan, bundleHash string) *manifest.Item {
	if plan.ResumeStage != task.StageDone || plan.LastValid == nil {
		return nil
	}
	var item manifest.Item
	if err := json.Unmarshal(plan.LastValid.Payload, &item); err != nil {
		return nil
	}
	if item.SHA256 != bundleHash {
		return nil
	}
	return &item
}
