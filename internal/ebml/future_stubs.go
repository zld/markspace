package ebml

import "pwflow/internal/fault"

// Temporary incremental-build stubs, replaced by dedicated files in the
// following module commits.
func (p *parser) parseTracks(start, end int64) error { return nil }

func (p *parser) parseCluster(h Header, end int64) (int64, error) { return end, nil }

func (p *parser) resyncCluster(badOff int64, code fault.Code, msg string) (Damage, error) {
	return Damage{}, fault.At(fault.EBMLResyncFailed, badOff, "%s", msg)
}
