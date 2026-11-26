package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mediaprep/internal/fixture"
	"mediaprep/internal/metadata"
	"mediaprep/internal/model"
	"mediaprep/internal/source"
	"mediaprep/internal/stream"
)

type CaseResult struct {
	Name          string `json:"name"`
	Conclusion    string `json:"conclusion"`
	Accepted      bool   `json:"accepted"`
	Code          string `json:"code,omitempty"`
	Track         string `json:"track,omitempty"`
	Offset        int64  `json:"offset,omitempty"`
	HasOffset     bool   `json:"has_offset"`
	Detail        string `json:"detail"`
	ResidualTasks int64  `json:"residual_tasks,omitempty"`
}

var AllVerifyCases = []string{
	"truncated-klv", "box-overrun", "crop-outside", "color-conflict", "cancel",
}

func RunVerifyCases(ctx context.Context, root string, names []string) ([]CaseResult, error) {
	if root == "" {
		root = filepath.Join(os.TempDir(), "mediaprep-verify")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	if len(names) == 0 {
		names = AllVerifyCases
	}
	results := make([]CaseResult, 0, len(names))
	var failures []error
	for _, name := range names {
		var r CaseResult
		var err error
		switch name {
		case "truncated-klv":
			r, err = verifyTruncatedKLV(root)
		case "box-overrun":
			r, err = verifyBoxOverrun(root)
		case "crop-outside":
			r, err = verifyCropOutside(root)
		case "color-conflict":
			r, err = verifyColorConflict(root)
		case "cancel":
			r, err = verifyCancel(ctx)
		default:
			err = fmt.Errorf("unknown verify case %q", name)
			r = CaseResult{Name: name, Conclusion: "UNKNOWN_CASE"}
		}
		if err != nil {
			failures = append(failures, err)
			r.Accepted = false
			if r.Detail == "" {
				r.Detail = err.Error()
			}
		}
		results = append(results, r)
	}
	return results, joinErrors(failures)
}

func verifyTruncatedKLV(root string) (CaseResult, error) {
	path := filepath.Join(root, "truncated-klv.mxf")
	if _, err := fixture.WriteMXF(fixture.MXFSpec{
		Path: path, PCM: make([]byte, 4800), TruncatedKLV: true,
	}); err != nil {
		return CaseResult{Name: "truncated-klv"}, err
	}
	_, err := source.ParseMXFFile(path)
	pe, ok := model.AsError(err)
	r := CaseResult{Name: "truncated-klv"}
	if !ok || pe.Code != model.CodeTruncated || pe.Track == "" || !pe.HasOffset {
		return r, fmt.Errorf("expected truncation with track/offset, got: %v", err)
	}
	r.Conclusion = "REJECTED_TRUNCATED"
	r.Accepted = true
	r.Code = string(pe.Code)
	r.Track = pe.Track
	r.Offset = pe.Offset
	r.HasOffset = true
	r.Detail = pe.Message
	return r, nil
}

func verifyBoxOverrun(root string) (CaseResult, error) {
	path := filepath.Join(root, "box-overrun.mp4")
	if _, err := fixture.WriteMP4(fixture.MP4Spec{
		Path: path, Payloads: [][]byte{[]byte("overrun")}, BoxOverrunInTrak: true,
	}); err != nil {
		return CaseResult{Name: "box-overrun"}, err
	}
	_, err := source.ParseMP4File(path)
	pe, ok := model.AsError(err)
	r := CaseResult{Name: "box-overrun"}
	if !ok || pe.Code != model.CodeLengthOutOfRange || pe.Track == "" || !pe.HasOffset {
		return r, fmt.Errorf("expected box overrun rejection with track/offset, got: %v", err)
	}
	r.Conclusion = "REJECTED_LENGTH_OUT_OF_RANGE"
	r.Accepted = true
	r.Code = string(pe.Code)
	r.Track = pe.Track
	r.Offset = pe.Offset
	r.HasOffset = true
	r.Detail = pe.Message
	return r, nil
}

func verifyCropOutside(root string) (CaseResult, error) {
	path := filepath.Join(root, "crop-outside.mp4")
	if _, err := fixture.WriteMP4(fixture.MP4Spec{
		Path: path, Width: 320, Height: 180, Payloads: [][]byte{[]byte("crop")},
		SampleFields: map[string]string{"crop": "0,0,999,999"},
	}); err != nil {
		return CaseResult{Name: "crop-outside"}, err
	}
	mf, err := source.ParseMP4File(path)
	if err != nil {
		return CaseResult{Name: "crop-outside"}, err
	}
	_, err = metadata.ResolveTrack(mf.Tracks[0])
	pe, ok := model.AsError(err)
	r := CaseResult{Name: "crop-outside"}
	if !ok || pe.Code != model.CodeInvalidMetadata || pe.Track == "" || !pe.HasOffset {
		return r, fmt.Errorf("expected crop rejection with track/offset, got: %v", err)
	}
	r.Conclusion = "REJECTED_CROP_OUTSIDE_FRAME"
	r.Accepted = true
	r.Code = string(pe.Code)
	r.Track = pe.Track
	r.Offset = pe.Offset
	r.HasOffset = true
	r.Detail = pe.Message
	return r, nil
}

func verifyColorConflict(root string) (CaseResult, error) {
	path := filepath.Join(root, "color-conflict.mp4")
	if _, err := fixture.WriteMP4(fixture.MP4Spec{
		Path: path, Payloads: [][]byte{[]byte("color")},
		SampleFields:    map[string]string{"color": "sample-bt709"},
		TrackFields:     map[string]string{"color": "track-bt601"},
		ContainerFields: map[string]string{"color": "container-bt2020"},
	}); err != nil {
		return CaseResult{Name: "color-conflict"}, err
	}
	mf, err := source.ParseMP4File(path)
	if err != nil {
		return CaseResult{Name: "color-conflict"}, err
	}
	resolved, err := metadata.ResolveTrack(mf.Tracks[0])
	if err != nil {
		return CaseResult{Name: "color-conflict"}, err
	}
	f, ok := resolved.Fields["color"]
	r := CaseResult{Name: "color-conflict", Track: mf.Tracks[0].Label}
	if !ok || !f.Conflict || f.Value != "sample-bt709" || len(resolved.Conflicts) != 1 {
		return r, fmt.Errorf("priority was not retained: %+v", resolved.Fields["color"])
	}
	r.Conclusion = "PRIORITY_VALUE_RETAINED"
	r.Accepted = true
	r.Offset = f.Offset
	r.HasOffset = true
	r.Detail = fmt.Sprintf("kept %q from %s; %d provenance values retained", f.Value, f.Scope, len(f.Alternatives))
	return r, nil
}

func verifyCancel(parent context.Context) (CaseResult, error) {
	pipe, err := stream.New(stream.Options{QueueLength: 1, BufferBudgetBytes: 2 * stream.BlockSize})
	if err != nil {
		return CaseResult{Name: "cancel"}, err
	}
	started := make(chan struct{}, 1)
	sink := func(ctx context.Context, _ stream.Block) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan error, 1)
	var metrics stream.Metrics
	go func() {
		metrics, err = pipe.Process(ctx, bytes.NewReader(make([]byte, 8*stream.BlockSize)), sink)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		return CaseResult{Name: "cancel"}, fmt.Errorf("sink did not start")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && pipe.Snapshot().BlocksProduced < 2 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			return CaseResult{Name: "cancel"}, fmt.Errorf("expected cancellation error")
		}
	case <-time.After(2 * time.Second):
		return CaseResult{Name: "cancel"}, fmt.Errorf("pipeline did not release blocked work")
	}
	metrics = pipe.Snapshot()
	r := CaseResult{
		Name: "cancel", Conclusion: "CANCELED_NO_RESIDUAL_TASKS", Accepted: metrics.ResidualTasks == 0,
		Detail: fmt.Sprintf("produced=%d consumed=%d dropped=%d residual=%d",
			metrics.BlocksProduced, metrics.BlocksConsumed, metrics.BlocksDropped, metrics.ResidualTasks),
		ResidualTasks: metrics.ResidualTasks,
	}
	if metrics.ResidualTasks != 0 {
		return r, fmt.Errorf("residual tasks=%d", metrics.ResidualTasks)
	}
	return r, nil
}

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	msg := ""
	for i, err := range errs {
		if i > 0 {
			msg += "; "
		}
		msg += err.Error()
	}
	return fmt.Errorf("%s", msg)
}
