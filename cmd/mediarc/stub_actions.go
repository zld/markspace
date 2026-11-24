package main

import (
	"errors"

	"github.com/zld/mediarc/internal/model"
)

// Temporary batch boundary keeps the demo action in the next reviewable patch.
func runDemo(_ []string) error {
	return &model.ConfigError{Field: "command", Err: errors.New("demo action pending")}
}
