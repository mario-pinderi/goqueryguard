package baseline

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

type Entry struct {
	Rule       string   `json:"rule"`
	Confidence string   `json:"confidence"`
	Package    string   `json:"package"`
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Function   string   `json:"function"`
	Terminal   string   `json:"terminal"`
	Chain      []string `json:"chain,omitempty"`
}

type File struct {
	Version  int     `json:"version"`
	Stats    Stats   `json:"stats"`
	Findings []Entry `json:"findings"`
}

type Set struct {
	keys map[string]struct{}
}

type Stats struct {
	Total         int            `json:"total"`
	ByConfidence  map[string]int `json:"by_confidence"`
	ByRule        map[string]int `json:"by_rule"`
	ByPackage     map[string]int `json:"by_package"`
	MaxChainDepth int            `json:"max_chain_depth"`
	AvgChainDepth float64        `json:"avg_chain_depth"`
}

func NewSet(entries []Entry) *Set {
	s := &Set{keys: make(map[string]struct{}, len(entries))}
	for _, e := range entries {
		s.keys[e.Key()] = struct{}{}
	}
	return s
}

func ComputeStats(entries []Entry) Stats {
	stats := Stats{
		Total:        len(entries),
		ByConfidence: make(map[string]int),
		ByRule:       make(map[string]int),
		ByPackage:    make(map[string]int),
	}
	totalDepth := 0
	for _, e := range entries {
		stats.ByConfidence[e.Confidence]++
		stats.ByRule[e.Rule]++
		stats.ByPackage[e.Package]++
		depth := len(e.Chain) - 1
		if depth < 0 {
			depth = 0
		}
		totalDepth += depth
		if depth > stats.MaxChainDepth {
			stats.MaxChainDepth = depth
		}
	}
	if stats.Total > 0 {
		stats.AvgChainDepth = float64(totalDepth) / float64(stats.Total)
	}
	return stats
}

func LoadSet(path string) (*Set, error) {
	if path == "" {
		return &Set{keys: map[string]struct{}{}}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Set{keys: map[string]struct{}{}}, nil
		}
		return nil, fmt.Errorf("read baseline file %q: %w", path, err)
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse baseline file %q: %w", path, err)
	}
	return NewSet(f.Findings), nil
}

func Write(path string, entries []Entry) error {
	if path == "" {
		return fmt.Errorf("baseline file path is empty")
	}
	type keyedEntry struct {
		entry Entry
		key   string
	}

	// Compute keys once so sorting does not repeatedly rebuild/hashes signatures.
	keyed := make([]keyedEntry, 0, len(entries))
	for _, e := range entries {
		keyed = append(keyed, keyedEntry{entry: e, key: e.Key()})
	}
	sort.Slice(keyed, func(i, j int) bool {
		return keyed[i].key < keyed[j].key
	})

	sortedEntries := make([]Entry, 0, len(keyed))
	for _, e := range keyed {
		sortedEntries = append(sortedEntries, e.entry)
	}

	f := File{Version: 1, Stats: ComputeStats(sortedEntries), Findings: sortedEntries}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal baseline: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write baseline %q: %w", path, err)
	}
	return nil
}

func (s *Set) Has(entry Entry) bool {
	if s == nil {
		return false
	}
	_, ok := s.keys[entry.Key()]
	return ok
}

func (e Entry) Key() string {
	chainLen := 0
	for _, seg := range e.Chain {
		chainLen += len(seg)
	}

	// Build a stable field signature, then hash it for compact baseline set membership checks.
	var b strings.Builder
	b.Grow(
		len(e.Rule) +
			len(e.Confidence) +
			len(e.Package) +
			len(e.File) +
			len(e.Function) +
			len(e.Terminal) +
			chainLen +
			len(e.Chain)*2 + // "->"
			64, // separators + line number
	)
	b.WriteString(e.Rule)
	b.WriteByte('|')
	b.WriteString(e.Confidence)
	b.WriteByte('|')
	b.WriteString(e.Package)
	b.WriteByte('|')
	b.WriteString(e.File)
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(e.Line))
	b.WriteByte('|')
	b.WriteString(e.Function)
	b.WriteByte('|')
	b.WriteString(e.Terminal)
	b.WriteByte('|')
	for i, seg := range e.Chain {
		if i > 0 {
			b.WriteString("->")
		}
		b.WriteString(seg)
	}

	sig := b.String()
	h := sha1.Sum([]byte(sig))
	return hex.EncodeToString(h[:])
}
