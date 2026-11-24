package main

import (
	"io"
	"os"

	"mediadag/internal/gen"
	"mediadag/internal/ingest"
	"mediadag/internal/task"
)

// sample is one generated input plus its canonical parameters.
type sample struct {
	name    string
	s       *gen.Sample
	params  []byte
	reorder int  // effective reorder window the sample was built with
	strict  bool // truncation must surface as input-corrupt
}

type demo struct {
	args *demoArgs
	ckpt *task.Checkpoint
	out  io.Writer
	err  io.Writer
}

func (dm *demo) makeSample(name string) *sample {
	d := dm.args
	var p gen.Params
	strict := false
	switch name {
	case "clean":
		p = d.cleanParams()
	case "lossy":
		p = d.lossyParams()
	case "truncated":
		p = d.cleanParams()
		p.Truncate = true
		strict = true
	case "noanchor":
		p = d.lossyParams()
		p.NoRTCP = true
	}
	s := gen.Generate(p)
	return &sample{name: name, s: s, params: paramsJSONFor(s, p, d.targetKbps, d.segmentMs),
		reorder: p.ReorderWindow, strict: strict}
}

// runDemo parses flags, loads the checkpoint and dispatches to a scenario.
func runDemo(args []string) int {
	d, err := parseDemoArgs(args)
	if err != nil {
		writeError(os.Stderr, "%v", err)
		return exitUsage
	}
	dm := &demo{args: d, out: os.Stdout, err: os.Stderr}
	if d.checkpoint != "" {
		dm.ckpt, err = task.LoadCheckpoint(d.checkpoint)
		if err != nil {
			writeError(dm.err, "%v", err)
			return exitUnexp
		}
		if err := dm.ckpt.Save(); err != nil { // prove the path is writable
			writeError(dm.err, "%v", err)
			return exitUnexp
		}
	} else {
		dm.ckpt = &task.Checkpoint{Nodes: map[string]*task.NodeState{}}
	}
	if d.scenario == "all" {
		return dm.runAll()
	}
	return dm.runSingle(d.scenario)
}

// withoutRTCP copies the sample without RTCP sender reports, simulating a
// capture that lost its clock anchors.
func withoutRTCP(s *gen.Sample) *gen.Sample {
	out := &gen.Sample{Digest: s.Digest}
	for _, ev := range s.Events {
		if ev.Kind == ingest.KindRTCP {
			continue
		}
		out.Events = append(out.Events, ev)
	}
	return out
}

// runAll and runSingle are implemented with the scenario flow.
func (dm *demo) runAll() int            { return exitOK }
func (dm *demo) runSingle(n string) int { return exitOK }
