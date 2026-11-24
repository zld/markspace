package main

import (
	"errors"

	"github.com/zld/mediarc/internal/model"
)

// Temporary batch boundaries keep each CLI patch reviewable and compilable.
func runDemo(_ []string) error {
	return &model.ConfigError{Field: "command", Err: errors.New("demo action pending")}
}

func runVerify(_ []string) error {
	return &model.ConfigError{Field: "command", Err: errors.New("verify action pending")}
}
