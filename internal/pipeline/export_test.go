package pipeline

import "mediaforge/internal/fixture"

// fixtureGenerate lets pipeline tests create captures without depending on
// the CLI.
func fixtureGenerate(dir string) error { return fixture.Generate(dir) }
