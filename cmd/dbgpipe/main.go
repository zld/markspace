package main

import (
	"fmt"

	"avbatch/internal/pipeline"
	"avbatch/internal/synth"
)

func main() {
	g, _ := synth.BuildDemoGroup(synth.Options{Seed: 41, InjectVFR: true})
	cfg := pipeline.DefaultConfig()
	res, runErr := pipeline.Run(pipeline.FromGroup(g), cfg)
	fmt.Println("err:", runErr)
	fmt.Println("segments:", res.Segments, "entries:", len(res.Entries))
	for i, e := range res.Entries {
		if i > 22 {
			break
		}
		fmt.Printf("%d kind=%s start=%d end=%d n=%d reason=%s\n", i, e.Kind, e.StartNanos, e.EndNanos, e.SampleCount, e.Reason)
	}
}
