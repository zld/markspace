package main

import (
	"fmt"

	"cmafrepair/pkg/media/fixture"
)

// runFixture generates the clean/edge/faults dataset tree.
func runFixture(args []string) error {
	f := parseFlags(args)
	out, ok := f.string_("out")
	if !ok || out == "" {
		return &exitError{code: exitInvalidInput, msg: "fixture requires --out DIR"}
	}
	seed := int64(7)
	if s, ok := f.string_("seed"); ok {
		n, err := parseInt64(s)
		if err != nil {
			return &exitError{code: exitInvalidInput, msg: "invalid --seed: " + err.Error()}
		}
		seed = n
	}
	sum, err := fixture.Generate(out, seed)
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	fmt.Printf("fixture root=%s seed=%d\n", sum.Root, sum.Seed)
	fmt.Printf("representations=%d fragments_per_rep=%d datasets=%v\n",
		sum.Representations, sum.FragmentsPerRep, sum.Datasets)
	return nil
}
