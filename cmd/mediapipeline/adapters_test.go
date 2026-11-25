package main

import (
	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/fixture"
)

// Small adapters keep the table-driven tests free of repeated conversions.
func caseType(s string) fixture.Case       { return fixture.Case(s) }
func stageType(s string) checkpoint.Stage { return checkpoint.Stage(s) }
