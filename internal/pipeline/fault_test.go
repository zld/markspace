package pipeline

import "mediaarchive/internal/diag"

func asFault(err error) (*diag.Fault, bool) { return diag.As(err) }
