package demo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"assetpack/internal/adapter"
	"assetpack/internal/clock"
	"assetpack/internal/index"
	"assetpack/internal/release"
	"assetpack/internal/samplegen"
	"assetpack/internal/stream"
	"assetpack/internal/timeline"
)

// Run executes generation → parse → index → range read → pipeline → release.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.Sequence == 0 {
		opts.Sequence = 1
	}
	if opts.QueueBlocks <= 0 {
		opts.QueueBlocks = 2
	}
	if opts.Range.To == 0 {
		opts.Range = clock.Range{From: 45000, To: 135000}
	}
	if err := opts.Range.Validate(180000); err != nil {
		return nil, clock.Wrap("demo", "range", int64(opts.Range.To), err)
	}
	if opts.Fault == "cancel" {
		opts.CancelAfter = 5 * time.Millisecond
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if opts.CancelAfter > 0 {
		time.AfterFunc(opts.CancelAfter, cancel)
	}

	if opts.Fault == "rtp-duplicate" || opts.Fault == "rtp-missing" {
		data, err := samplegen.BuildRTPStream(opts.Fault)
		if err != nil {
			return nil, err
		}
		a, _ := adapter.DefaultRegistry().Format("rtp/v1")
		if _, err := a.Parse(runCtx, adapter.Source{Name: "rtp-fixture", Data: data}); err != nil {
			return nil, err
		}
	}

	built, err := samplegen.BuildContainer(opts.Sequence, false)
	if err != nil {
		return nil, err
	}
	data := built.Data
	sourceFormat := "mp4/v1"
	switch opts.Fault {
	case "bad-mdat":
		data = samplegen.TruncateMDAT(data, 128)
	case "unknown-version":
		data, err = samplegen.UnknownVersionContainer()
		if err != nil {
			return nil, err
		}
	case "fmp4":
		fb, err := samplegen.BuildContainer(opts.Sequence, true)
		if err != nil {
			return nil, err
		}
		data = fb.Data
		sourceFormat = "fmp4/v1"
	case "publish":
		return nil, fmt.Errorf("%w: injected target verification failure", release.ErrPublish)
	}

	reg := adapter.DefaultRegistry()
	parser, err := reg.Format(sourceFormat)
	if err != nil {
		return nil, err
	}
	asset, err := parser.Parse(runCtx, adapter.Source{Name: "input.mp4", Data: data})
	if err != nil {
		return nil, err
	}
	tl, err := timeline.Compile(Plan(opts.Sequence))
	if err != nil {
		return nil, err
	}

	idx, err := index.Build(asset, "inputs/input.mp4")
	if err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp("", "assetpack-index-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	if _, err := idx.Save(work); err != nil {
		return nil, err
	}
	loaded, err := index.Load(filepath.Join(work, index.IndexFileName))
	if err != nil {
		return nil, err
	}
	indexBytes, err := os.ReadFile(filepath.Join(work, index.IndexFileName))
	if err != nil {
		return nil, err
	}

	reader := index.NewReader(loaded, bytes.NewReader(data))
	videoCoverage, err := reader.ReadRange(runCtx, "video", opts.Range, true)
	if err != nil {
		return nil, err
	}
	audioCoverage, err := reader.ReadRange(runCtx, "audio", opts.Range, true)
	if err != nil {
		return nil, err
	}
	audioBytes := concatenatePayloads(audioCoverage.Payload)

	files := map[string][]byte{
		"inputs/input.mp4": data,
		"index.json":       indexBytes,
	}
	var filesMu sync.Mutex
	sourceByIndex := map[int][]byte{}
	for _, p := range videoCoverage.Payload {
		sourceByIndex[p.Index] = p.Payload
	}
	cutFrame := int(tl.Transitions[0].EditPoint / 3750)

	staging, err := stream.NewStaging(opts.Out, ".pipeline-")
	if err != nil {
		return nil, err
	}
	defer staging.Abort()

	pipe := &stream.Pipeline{
		Queue:   stream.NewQueue(opts.QueueBlocks),
		Staging: staging,
		Produce: func(ctx context.Context, emit func(stream.Block) error) error {
			for i, p := range videoCoverage.Payload {
				if err := emit(stream.Block{Index: i, Key: fmt.Sprintf("frame-%05d", i+1), Data: p.Payload}); err != nil {
					return err
				}
			}
			return nil
		},
		Consume: stream.SlowConsume(time.Duration(opts.SlowConsumerMS)*time.Millisecond, func(b stream.Block) error {
			rec := videoCoverage.Records[b.Index]
			frame := renderFrame(b.Index, rec.Index, b.Data, sourceByIndex, cutFrame)
			desktopPath := fmt.Sprintf("desktop/frame-%05d.rgba", b.Index+1)
			filesMu.Lock()
			files[desktopPath] = frame
			if b.Index%2 == 0 {
				mobilePath := fmt.Sprintf("mobile/frame-%05d.rgba", b.Index/2+1)
				files[mobilePath] = frame
			}
			filesMu.Unlock()
			f, err := staging.CreateFile(fmt.Sprintf("blocks/frame-%05d.rgba", b.Index+1))
			if err != nil {
				return err
			}
			if _, err := f.Write(frame); err != nil {
				f.Close()
				return err
			}
			return f.Close()
		}),
	}
	stats, err := pipe.Run(runCtx)
	if err != nil {
		return nil, err
	}

	return commitPackage(opts, tl, videoCoverage, audioBytes, files, stats)
}

func concatenatePayloads(ps []index.PayloadRecord) []byte {
	n := 0
	for _, p := range ps {
		n += len(p.Payload)
	}
	out := make([]byte, 0, n)
	for _, p := range ps {
		out = append(out, p.Payload...)
	}
	return out
}

// QueryRange loads the active disk index and performs a located range query.
func QueryRange(ctx context.Context, root string, r clock.Range) (*index.Coverage, error) {
	current := filepath.Join(root, release.CurrentLink)
	idx, err := index.Load(filepath.Join(current, index.IndexFileName))
	if err != nil {
		return nil, err
	}
	sourcePath := idx.ResolveSource(current)
	f, err := os.Open(sourcePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader := index.NewReader(idx, f)
	return reader.ReadRange(ctx, "video", r, false)
}
