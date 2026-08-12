// Package gatepolicy holds application-layer GatePolicy implementations
// that need to read committed domain artifacts (plan, risk policy) via git —
// concerns internal/gate deliberately stays free of (architecture freeze:
// internal/gate imports no domain package). Gate2Policy is the first
// consumer: it composes internal/gate (GatePolicy contract), internal/plan
// (Plan/RiskPolicy/VerifyLineage) and internal/spec (PlanScope) to implement
// the Gate 2 decision rules (§3.0/§3.3/§3.9).
package gatepolicy

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/slam0504/sdlc-workbench/internal/gate"
	"github.com/slam0504/sdlc-workbench/internal/plan"
	"github.com/slam0504/sdlc-workbench/internal/spec"
)

// tierOrder mirrors internal/plan's private tierOrder (low < medium < high).
// Duplicated here rather than exported from plan, per the architecture
// freeze: plan stays domain-only and gatepolicy is the one place that needs
// to compare tiers across the gate2 decision boundary.
var tierOrder = map[string]int{"low": 1, "medium": 2, "high": 3}

var (
	reSHA256 = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reGitOID = regexp.MustCompile(`^git:(sha1:[0-9a-f]{40}|sha256:[0-9a-f]{64})$`)
)

// bindingReq is gatepolicy's own copy of gate's private BindingReq/
// validateBindingSet machinery — internal/gate does not export a helper
// usable from here, and duplicating this small check is simpler than adding
// an export surface to internal/gate for a single caller.
type bindingReq struct {
	kind, role string
	digestRe   *regexp.Regexp
}

var gate2BindingReqs = []bindingReq{
	{kind: "spec_manifest", digestRe: reSHA256},
	{kind: "plan", digestRe: reSHA256},
	{kind: "base_commit", digestRe: reGitOID},
	{kind: "risk_policy", digestRe: reSHA256},
	{kind: "permission_manifest", digestRe: reSHA256},
}

// PlanLoader reads the committed plan and risk policy as of commitOID
// (typically the plan_commit bound to a Gate 2 request/record).
type PlanLoader interface {
	LoadAt(commitOID, planID string) (plan.Plan, plan.RiskPolicy, error)
}

// Gate2Policy implements gate.GatePolicy for gate2 (§3.3/§3.9).
type Gate2Policy struct {
	loader PlanLoader
	git    plan.GitRunner

	currentPlanManifest       func() (string, error)
	currentSpecManifest       func() (string, error)
	currentRiskPolicyDigest   func() (string, error)
	currentPermissionManifest func() (string, error)
}

var _ gate.GatePolicy = (*Gate2Policy)(nil)

// NewGate2Policy returns the GatePolicy for gate2. loader reads the
// committed plan/risk policy at a given commit; g runs the git lineage
// checks (§3.0); the four current* functions each read the digest of the
// artifact ReconcileBindings compares against (§3.9's "持續重算" class).
func NewGate2Policy(loader PlanLoader, g plan.GitRunner,
	currentPlanManifest func() (string, error),
	currentSpecManifest func() (string, error),
	currentRiskPolicyDigest func() (string, error),
	currentPermissionManifest func() (string, error)) gate.GatePolicy {
	return &Gate2Policy{
		loader: loader, git: g,
		currentPlanManifest:       currentPlanManifest,
		currentSpecManifest:       currentSpecManifest,
		currentRiskPolicyDigest:   currentRiskPolicyDigest,
		currentPermissionManifest: currentPermissionManifest,
	}
}

// ValidateRequest checks the gate2 binding schema, the subject shape
// ("plan:<plan_id>"), and the §3.0 lineage invariant: the committed plan's
// analysis_base_commit must be an ancestor of plan_commit (the base_commit
// binding), and every path touched in that range must be under plan/**.
func (p *Gate2Policy) ValidateRequest(req gate.GateRequest) error {
	planID, ok := planIDFromSubject(req.Subject)
	if !ok {
		return fmt.Errorf("gatepolicy: gate2 subject must have prefix %q, got %q", "plan:", req.Subject)
	}
	if err := validateGate2Bindings(req.Bindings); err != nil {
		return err
	}
	planCommit := gitOID(bindingDigest(req.Bindings, "base_commit"))
	pl, _, err := p.loader.LoadAt(planCommit, planID)
	if err != nil {
		return fmt.Errorf("gatepolicy: load plan at %s: %w", planCommit, err)
	}
	if err := plan.VerifyLineage(p.git, pl.AnalysisBaseCommit, planCommit, spec.PlanScope.Match); err != nil {
		return fmt.Errorf("gatepolicy: %w", err)
	}
	return nil
}

// BuildDecision validates the per-task risk selections against the
// committed plan and assembles the task_id-sorted Metadata.RiskDecisions
// (§3.3). rejected decisions must carry no risk selections.
func (p *Gate2Policy) BuildDecision(req gate.GateRequest, decision string, input gate.DecisionInput) (*gate.Metadata, error) {
	if decision == "rejected" {
		if len(input.RiskSelections) > 0 {
			return nil, fmt.Errorf("gatepolicy: gate2 rejected decision must not include risk selections")
		}
		return nil, nil
	}
	if decision != "approved" {
		return nil, fmt.Errorf("gatepolicy: gate2 unknown decision %q", decision)
	}

	planID, ok := planIDFromSubject(req.Subject)
	if !ok {
		return nil, fmt.Errorf("gatepolicy: gate2 subject must have prefix %q, got %q", "plan:", req.Subject)
	}
	planCommit := gitOID(bindingDigest(req.Bindings, "base_commit"))
	pl, pol, err := p.loader.LoadAt(planCommit, planID)
	if err != nil {
		return nil, fmt.Errorf("gatepolicy: load plan at %s: %w", planCommit, err)
	}

	selByTask := make(map[string]gate.RiskSelection, len(input.RiskSelections))
	for _, sel := range input.RiskSelections {
		if _, dup := selByTask[sel.TaskID]; dup {
			return nil, fmt.Errorf("gatepolicy: duplicate risk selection for task %q", sel.TaskID)
		}
		selByTask[sel.TaskID] = sel
	}

	decisions := make([]gate.RiskDecision, 0, len(pl.Tasks))
	for _, t := range pl.Tasks {
		sel, ok := selByTask[t.ID]
		if !ok {
			return nil, fmt.Errorf("gatepolicy: missing risk selection for task %q", t.ID)
		}
		delete(selByTask, t.ID)

		minimum := pol.ComputeMinimum(t)
		if minimum != t.MinimumRiskTier {
			return nil, fmt.Errorf("gatepolicy: task %q: recomputed minimum_risk_tier %q does not match committed %q", t.ID, minimum, t.MinimumRiskTier)
		}
		minRank, ok := tierOrder[minimum]
		if !ok {
			return nil, fmt.Errorf("gatepolicy: task %q: unknown minimum_risk_tier %q", t.ID, minimum)
		}
		plannerRank, ok := tierOrder[t.PlannerRiskTier]
		if !ok {
			return nil, fmt.Errorf("gatepolicy: task %q: unknown planner_risk_tier %q", t.ID, t.PlannerRiskTier)
		}
		if plannerRank < minRank {
			return nil, fmt.Errorf("gatepolicy: task %q: planner_risk_tier %q below minimum_risk_tier %q", t.ID, t.PlannerRiskTier, minimum)
		}
		selRank, ok := tierOrder[sel.SelectedRiskTier]
		if !ok {
			return nil, fmt.Errorf("gatepolicy: task %q: unknown selected_risk_tier %q", t.ID, sel.SelectedRiskTier)
		}
		if selRank < minRank {
			return nil, fmt.Errorf("gatepolicy: task %q: selected_risk_tier %q below minimum_risk_tier %q", t.ID, sel.SelectedRiskTier, minimum)
		}
		if selRank < plannerRank && sel.OverrideReason == "" {
			return nil, fmt.Errorf("gatepolicy: task %q: selected_risk_tier %q below planner_risk_tier %q requires override_reason", t.ID, sel.SelectedRiskTier, t.PlannerRiskTier)
		}
		decisions = append(decisions, gate.RiskDecision{
			TaskID:           t.ID,
			MinimumRiskTier:  minimum,
			PlannerRiskTier:  t.PlannerRiskTier,
			SelectedRiskTier: sel.SelectedRiskTier,
			OverrideReason:   sel.OverrideReason,
		})
	}
	if len(selByTask) > 0 {
		extra := make([]string, 0, len(selByTask))
		for id := range selByTask {
			extra = append(extra, id)
		}
		sort.Strings(extra)
		return nil, fmt.Errorf("gatepolicy: risk selections reference unknown task ids: %v", extra)
	}

	sort.Slice(decisions, func(i, j int) bool { return decisions[i].TaskID < decisions[j].TaskID })
	return &gate.Metadata{RiskDecisions: decisions}, nil
}

// SupersessionKey uses the default gate+"|"+subject formula (same as gate1).
func (p *Gate2Policy) SupersessionKey(gateName, subject string) string {
	return gateName + "|" + subject
}

// exitCoder matches *exec.ExitError's ExitCode() method structurally — same
// trick as plan.VerifyLineage — so a "rev-parse --verify" exit 1 (commit
// missing) can be told apart from a fatal/genuine command failure (exit 128
// or non-ExitError) without importing os/exec.
type exitCoder interface{ ExitCode() int }

// ReconcileBindings compares each continuously-recomputed binding
// (spec_manifest/plan/risk_policy/permission_manifest) against its current
// digest, and verifies base_commit still exists as a commit — it must NOT
// go stale just because HEAD moved past it (§3.9 "歷史錨點"). Any current*
// read error is returned as-is (fail closed, not a permanent stale cause).
func (p *Gate2Policy) ReconcileBindings(rec gate.ApprovalRecord) ([]gate.StaleCause, error) {
	var causes []gate.StaleCause

	recomputed := []struct {
		kind string
		cur  func() (string, error)
	}{
		{"spec_manifest", p.currentSpecManifest},
		{"plan", p.currentPlanManifest},
		{"risk_policy", p.currentRiskPolicyDigest},
		{"permission_manifest", p.currentPermissionManifest},
	}
	for _, c := range recomputed {
		cur, err := c.cur()
		if err != nil {
			return nil, err // fail closed (§3.9)
		}
		if bound := bindingDigest(rec.Bindings, c.kind); bound != "" && bound != cur {
			causes = append(causes, gate.StaleCause{Cause: c.kind + " changed", EvidenceRef: cur})
		}
	}

	if baseDigest := bindingDigest(rec.Bindings, "base_commit"); baseDigest != "" {
		oid := gitOID(baseDigest)
		// `git cat-file -e` exits 128 for BOTH "commit missing" and fatal
		// repo errors, so it cannot tell staleness from a read failure.
		// `rev-parse --verify --quiet` exits 1 for "not a valid commit" and
		// 128 for fatal errors — the same ancestor/fatal split
		// VerifyLineage relies on (lineage.go) — so only exit 1 is staleness.
		if _, err := p.git.Git("rev-parse", "--verify", "--quiet", oid+"^{commit}"); err != nil {
			var ec exitCoder
			if !errors.As(err, &ec) || ec.ExitCode() != 1 {
				return nil, err // fatal/unrecognized failure: fail closed, not a stale cause
			}
			causes = append(causes, gate.StaleCause{Cause: "base_commit missing", EvidenceRef: oid})
		}
	}

	return causes, nil
}

func planIDFromSubject(subject string) (string, bool) {
	id, ok := strings.CutPrefix(subject, "plan:")
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

func bindingDigest(bs []gate.Binding, kind string) string {
	for _, b := range bs {
		if b.Kind == kind && b.Role == "" {
			return b.Digest
		}
	}
	return ""
}

// gitOID strips the "git:sha1:"/"git:sha256:" digest prefix, since
// plan.GitRunner/plan.VerifyLineage and `git cat-file` both expect a bare
// OID, not the binding's typed digest form.
func gitOID(digest string) string {
	if s, ok := strings.CutPrefix(digest, "git:sha1:"); ok {
		return s
	}
	if s, ok := strings.CutPrefix(digest, "git:sha256:"); ok {
		return s
	}
	return digest
}

func validateGate2Bindings(bs []gate.Binding) error {
	seen := map[[2]string]bool{}
	for _, b := range bs {
		key := [2]string{b.Kind, b.Role}
		if seen[key] {
			return fmt.Errorf("gatepolicy: duplicate binding (kind,role): (%q,%q)", b.Kind, b.Role)
		}
		seen[key] = true
	}
	for _, req := range gate2BindingReqs {
		found := false
		for _, b := range bs {
			if b.Kind == req.kind && b.Role == req.role {
				found = true
				if !req.digestRe.MatchString(b.Digest) {
					return fmt.Errorf("gatepolicy: binding (%q,%q) digest %q does not match expected pattern", req.kind, req.role, b.Digest)
				}
				break
			}
		}
		if !found {
			return fmt.Errorf("gatepolicy: missing required binding (kind,role): (%q,%q)", req.kind, req.role)
		}
	}
	return nil
}
