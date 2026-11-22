package checkpoint

import "github.com/pairwise/mediad/internal/fault"

func exitOf(err error) int { return fault.ExitCode(err) }
