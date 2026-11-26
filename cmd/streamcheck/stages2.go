package main

import (
	"streamcheck/core"
	"streamcheck/fixture"
	"streamcheck/pipeline"
)

// inspectStage and migrateStage are implemented in a follow-up patch;
// these stubs keep the tree compilable.
func inspectStage(work string) pipeline.Stage {
	return pipeline.Stage{Name: "inspect", Run: func(map[string]string) (string, error) {
		return "", core.E(core.KindInspect, "inspect", "not implemented", nil)
	}}
}

func migrateStage(work string, plan *fixture.Plan) pipeline.Stage {
	return pipeline.Stage{Name: "migrate", Run: func(map[string]string) (string, error) {
		return "", core.E(core.KindMigrate, "migrate", "not implemented", nil)
	}}
}
