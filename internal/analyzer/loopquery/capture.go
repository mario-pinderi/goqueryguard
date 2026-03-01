package loopquery

import (
	"sync"

	"github.com/mario-pinderi/goqueryguard/internal/baseline"
)

// FindingSink receives normalized findings emitted by the analyzer.
// Implementations must be safe for concurrent use.
type FindingSink interface {
	Add(entry baseline.Entry) error
}

// FindingCollector stores emitted findings in memory.
type FindingCollector struct {
	mu      sync.Mutex
	entries []baseline.Entry
}

func NewFindingCollector() *FindingCollector {
	return &FindingCollector{entries: make([]baseline.Entry, 0, 64)}
}

func (c *FindingCollector) Add(entry baseline.Entry) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, entry)
	return nil
}

func (c *FindingCollector) Entries() []baseline.Entry {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]baseline.Entry, len(c.entries))
	copy(out, c.entries)
	return out
}

func capturePackageAllowed(pkgPath string, allow map[string]struct{}) bool {
	if len(allow) == 0 {
		return true
	}
	_, ok := allow[pkgPath]
	return ok
}
