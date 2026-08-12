package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// Scope parameterizes the managed-tree boundary that manifest digesting and
// GitRepo's scoped git operations operate over. spec/ and plan/ (Task 8+)
// share this machinery instead of each hardcoding roots/patterns/matching.
type Scope struct {
	Version  int
	Patterns []string // part of the canonical manifest content (ManifestDigest)
	Roots    []string // git pathspec roots — same role as the old managedScopeRoots
	Match    func(rel string) bool
}

// SpecScope is the existing managed spec/ tree boundary, expressed as a
// Scope. Match delegates to specInScope (the pre-Task-7 InScope logic) —
// InScope itself now delegates to SpecScope.Match, so this must NOT be
// InScope directly or the two would recurse into each other.
var SpecScope = Scope{Version: ScopeVersion, Patterns: ScopePatterns, Roots: managedScopeRoots, Match: specInScope}

// PlanScope is the plan/ tree boundary (Task 8+ plan.yaml artifacts).
var PlanScope = Scope{
	Version:  1,
	Patterns: []string{"plan/**"},
	Roots:    []string{"plan"},
	Match: func(rel string) bool {
		rel = strings.TrimPrefix(rel, "./")
		return rel == "plan" || strings.HasPrefix(rel, "plan/")
	},
}

// ManifestDigest computes the canonical digest for entries under this scope
// — scope Version and Patterns are part of the canonical content, so a
// digest computed under one scope can never collide with another scope's
// digest over the same entries, and changing a scope's version/patterns
// changes every digest computed under it.
func (sc Scope) ManifestDigest(entries []FileEntry) (string, error) {
	files := append([]FileEntry(nil), entries...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	c := canonical{ScopeVersion: sc.Version, Patterns: sc.Patterns, Files: files}
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
