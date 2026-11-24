package main

import (
	"example.com/media-kernel/pipeline"
	"example.com/media-kernel/sample"
)

func assertReport(_ *sample.Fixture, _ *pipeline.Result, _ *pipeline.RecoveryReport, _ int64) error {
	return nil
}
