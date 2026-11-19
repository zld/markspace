package main

import (
	"bytes"
	"encoding/json"
	"net"
	"testing"
	"time"

	"jitterlab/internal/scenario"
)

type jsonReport struct {
	Mode    string `json:"mode"`
	Summary struct {
		Received         uint64   `json:"received"`
		DuplicateDropped uint64   `json:"duplicate_dropped"`
		LateDropped      uint64   `json:"late_dropped"`
		Malformed        uint64   `json:"malformed"`
		ConcealedFrames  uint64   `json:"concealed_frames"`
		MaxConcealRun    int      `json:"max_conceal_run"`
		ContinuityRatio  *float64 `json:"continuity_ratio"`
	} `json:"summary"`
	Watermarks []json.RawMessage `json:"watermarks"`
}

func TestReplayAcceptanceShape(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{
		"replay", "--frames", "1200", "--seed", "41", "--loss-burst", "7",
		"--reorder-depth", "9", "--duplicate-rate", "0.03",
		"--jitter-ms", "18", "--drift-ppm", "120", "--json",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	var r jsonReport
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(r.Watermarks) < 100 {
		t.Fatalf("watermarks=%d", len(r.Watermarks))
	}
	s := r.Summary
	if s.DuplicateDropped == 0 || s.LateDropped == 0 || s.ConcealedFrames == 0 {
		t.Fatalf("dup=%d late=%d concealed=%d", s.DuplicateDropped, s.LateDropped, s.ConcealedFrames)
	}
	if s.MaxConcealRun <= 0 {
		t.Fatalf("max_conceal_run=%d", s.MaxConcealRun)
	}
	if s.ContinuityRatio == nil || *s.ContinuityRatio <= 0 || *s.ContinuityRatio >= 1 {
		t.Fatalf("continuity=%v", s.ContinuityRatio)
	}
}

func TestReplayDeterministic(t *testing.T) {
	args := []string{"replay", "--frames", "300", "--seed", "99", "--json"}
	var a, b bytes.Buffer
	if run(args, &a, &bytes.Buffer{}) != 0 || run(args, &b, &bytes.Buffer{}) != 0 {
		t.Fatal("replay failed")
	}
	if a.String() != b.String() {
		t.Fatal("same seed must reproduce identical reports")
	}
}

func TestInvalidArgsExit2(t *testing.T) {
	cases := [][]string{
		{},
		{"bogus"},
		{"replay", "--frames", "0"},
		{"replay", "--duplicate-rate", "1.5"},
		{"listen", "--duration", "-1s"},
	}
	for _, args := range cases {
		if code := run(args, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
			t.Fatalf("args %v: exit=%d want 2", args, code)
		}
	}
}

func TestListenWithLiveSender(t *testing.T) {
	addr := "127.0.0.1:39177"
	out := make(chan bytes.Buffer, 1)
	code := make(chan int, 1)
	go func() {
		var buf bytes.Buffer
		code <- run([]string{"listen", "--addr", addr, "--duration", "800ms",
			"--sample-interval-ms", "20", "--json"}, &buf, &bytes.Buffer{})
		out <- buf
	}()
	time.Sleep(100 * time.Millisecond) // let the socket bind

	evs := scenario.Generate(scenario.Config{
		Frames: 60, Seed: 5, StreamID: 3, LossBurst: 2, ReorderDepth: 2,
		DuplicateRate: 0.05, JitterMs: 2, FrameInterval: 5 * time.Millisecond,
	})
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	start := time.Now()
	for _, e := range evs {
		if d := time.Until(start.Add(e.At)); d > 0 {
			time.Sleep(d)
		}
		conn.Write(e.Data)
	}

	if c := <-code; c != 0 {
		t.Fatalf("listen exit=%d", c)
	}
	var r jsonReport
	buf := <-out
	if err := json.Unmarshal(buf.Bytes(), &r); err != nil {
		t.Fatalf("json: %v", err)
	}
	if r.Mode != "listen" || r.Summary.Received == 0 {
		t.Fatalf("mode=%s received=%d", r.Mode, r.Summary.Received)
	}
	if len(r.Watermarks) == 0 {
		t.Fatal("expected watermark samples from live playout")
	}
}
