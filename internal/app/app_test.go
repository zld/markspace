package app

import (
	"context"
	"path/filepath"
	"testing"

	"rateqa/internal/cli"
	"rateqa/internal/media"
)

func TestAppDemoComplete(t *testing.T) {
	if err := media.EnsureFFmpeg(context.Background()); err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	ws := t.TempDir()
	opts := cli.Options{Command: cli.CmdDemo, Workspace: ws, Segments: 2,
		FramesPerSegment: 12, TargetKbps: 600, Workers: 2}
	if code := Run(context.Background(), opts); code != cli.ExitOK {
		t.Fatalf("demo exit code: %d", code)
	}
}

func TestAppDemoFailThenResume(t *testing.T) {
	if err := media.EnsureFFmpeg(context.Background()); err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	ws := t.TempDir()
	fail := cli.Options{Command: cli.CmdDemo, Workspace: ws, Segments: 3,
		FramesPerSegment: 12, TargetKbps: 600, Workers: 3, FailAfter: 3}
	if code := Run(context.Background(), fail); code != cli.ExitInterrupted {
		t.Fatalf("want exit 75, got %d", code)
	}
	resume := cli.Options{Command: cli.CmdResume, Workspace: ws, Workers: 2,
		FramesPerSegment: 12, TargetKbps: 600}
	if code := Run(context.Background(), resume); code != cli.ExitOK {
		t.Fatalf("resume exit: %d", code)
	}
	if code := Run(context.Background(),
		cli.Options{Command: cli.CmdReport, Workspace: ws}); code != cli.ExitOK {
		t.Fatalf("report exit: %d", code)
	}
}

func TestAppResumeRejectsMismatch(t *testing.T) {
	if err := media.EnsureFFmpeg(context.Background()); err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	ws := t.TempDir()
	first := cli.Options{Command: cli.CmdDemo, Workspace: ws, Segments: 1,
		FramesPerSegment: 8, TargetKbps: 600, Workers: 1}
	if code := Run(context.Background(), first); code != cli.ExitOK {
		t.Fatalf("seed run: %d", code)
	}
	// Resume with altered target must be refused (parameter mismatch).
	bad := cli.Options{Command: cli.CmdResume, Workspace: ws, Workers: 1,
		FramesPerSegment: 8, TargetKbps: 999}
	if code := Run(context.Background(), bad); code == cli.ExitOK {
		t.Fatal("mismatched resume should fail")
	}
}

func TestAppRunFromY4M(t *testing.T) {
	if err := media.EnsureFFmpeg(context.Background()); err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	src := filepath.Join(dir, "clip.y4m")
	if _, err := media.GenerateDemo(media.DemoConfig{Path: src, Width: 64, Height: 48,
		FPSNum: 25, FPSDen: 1, Segments: 2, FramesPerSegment: 10}); err != nil {
		t.Fatal(err)
	}
	opts := cli.Options{Command: cli.CmdRun, Workspace: ws, Input: src,
		FramesPerSegment: 10, TargetKbps: 500, Workers: 1}
	if code := Run(context.Background(), opts); code != cli.ExitOK {
		t.Fatalf("run exit: %d", code)
	}
}

func TestAppReportMissingWorkspace(t *testing.T) {
	code := Run(context.Background(), cli.Options{
		Command: cli.CmdReport, Workspace: t.TempDir()})
	if code != cli.ExitStorage {
		t.Fatalf("want exit 4, got %d", code)
	}
}
