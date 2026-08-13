// Package evidence declares the oracle-surface boundary (§3.6): the set of
// test/oracle files whose content must not silently drift between the plan
// commit and the evidence commit. OracleDecl reuses spec.Scope's canonical
// manifest machinery so the digest format and STALE semantics stay identical
// to spec/plan scopes.
package evidence

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/slam0504/sdlc-workbench/internal/spec"
)

// OracleDecl is the parsed form of plan/oracle-surface.yaml (§3.6). Patterns
// use the same two pattern forms as spec.Scope: a directory prefix ("dir/**")
// or an exact file path — no mid-segment or single-segment glob, no absolute
// path. ParseOracleDecl rejects anything else at parse time so an
// unsupported pattern can never silently under- or over-match later.
type OracleDecl struct {
	Version  int      `yaml:"version"`
	Patterns []string `yaml:"patterns"`
}

// ParseOracleDecl decodes an oracle-surface declaration, rejecting unknown
// fields and any pattern outside the frozen dir/** / exact-file-path
// semantics.
func ParseOracleDecl(b []byte) (OracleDecl, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var d OracleDecl
	if err := dec.Decode(&d); err != nil {
		return OracleDecl{}, fmt.Errorf("evidence: parse oracle decl: %w", err)
	}
	if len(d.Patterns) == 0 {
		return OracleDecl{}, fmt.Errorf("evidence: parse oracle decl: no patterns declared")
	}
	for _, p := range d.Patterns {
		if err := validateOraclePattern(p); err != nil {
			return OracleDecl{}, fmt.Errorf("evidence: parse oracle decl: %w", err)
		}
	}
	return d, nil
}

// validateOraclePattern enforces the frozen M3a pattern semantics: only a
// directory prefix ("dir/**", dir itself free of wildcards) or an exact file
// path (no wildcard characters at all) is accepted. Mid-segment wildcards
// (internal/*/testdata/**), single-segment globs (*.go), empty patterns,
// absolute paths, and any path-traversal segment (".." or ".") are all
// rejected here so they can never reach Match/Scope — Scope().Roots feeds
// downstream git pathspecs (ScopedClean, ReadScopedHeadTree/Worktree), and a
// ".." there must never be allowed to escape the scoped tree.
func validateOraclePattern(p string) error {
	if p == "" {
		return fmt.Errorf("empty oracle pattern")
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("oracle pattern must be relative, got %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." {
			return fmt.Errorf("oracle pattern must not contain path-traversal segment %q, got %q", seg, p)
		}
	}
	if strings.HasSuffix(p, "/**") {
		dir := strings.TrimSuffix(p, "/**")
		if dir == "" || strings.Contains(dir, "*") {
			return fmt.Errorf("invalid dir-prefix oracle pattern %q (want dir/** with no wildcard in dir)", p)
		}
		return nil
	}
	if strings.Contains(p, "*") {
		return fmt.Errorf("unsupported oracle pattern %q (only dir/** or an exact file path is allowed)", p)
	}
	return nil
}

// Match reports whether rel falls under this declaration's patterns.
// Semantics mirror spec.InScope: a "dir/**" pattern matches dir itself and
// everything under it; any other pattern must match rel exactly.
func (d OracleDecl) Match(rel string) bool {
	rel = strings.TrimPrefix(rel, "./")
	for _, p := range d.Patterns {
		if dir, ok := strings.CutSuffix(p, "/**"); ok {
			if rel == dir || strings.HasPrefix(rel, dir+"/") {
				return true
			}
			continue
		}
		if rel == p {
			return true
		}
	}
	return false
}

// Scope converts this declaration into a spec.Scope so callers can reuse
// spec's canonical ManifestDigest / GitRepo scoped-tree machinery instead of
// duplicating it. Roots are derived per pattern: a "dir/**" pattern
// contributes dir, an exact file path contributes itself.
func (d OracleDecl) Scope() spec.Scope {
	roots := make([]string, len(d.Patterns))
	for i, p := range d.Patterns {
		if dir, ok := strings.CutSuffix(p, "/**"); ok {
			roots[i] = dir
		} else {
			roots[i] = p
		}
	}
	return spec.Scope{
		Version:  d.Version,
		Patterns: d.Patterns,
		Roots:    roots,
		Match:    d.Match,
	}
}

// OracleDigestAt computes the canonical manifest digest of d's in-scope
// files as they exist in commitOID's tree.
//
// r's scope must already be d.Scope() — i.e. r must have been built via
// spec.NewGitRepo(root, d.Scope()) — because GitRepo filters
// ReadScopedHeadTree's walk through the scope injected at construction time,
// not through d.Match directly. This keeps the signature identical to the
// brief (r *spec.GitRepo, not a bare root string) while making the
// precondition explicit here rather than leaving it implicit at call sites.
func OracleDigestAt(r *spec.GitRepo, d OracleDecl, commitOID string) (string, error) {
	entries, err := r.ReadScopedHeadTree(commitOID)
	if err != nil {
		return "", fmt.Errorf("evidence: oracle digest at %s: %w", commitOID, err)
	}
	return d.Scope().ManifestDigest(entries)
}
