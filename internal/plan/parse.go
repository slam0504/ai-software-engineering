package plan

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Parse decodes a plan document. It rejects any field not in the Plan
// schema — in particular selected_risk_tier/override_reason, which belong
// to the gate2 decision record, not the plan (§3.3: this is the single
// authoritative line of defense keeping gate-decision fields out of plans).
func Parse(b []byte) (Plan, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var p Plan
	if err := dec.Decode(&p); err != nil {
		return Plan{}, fmt.Errorf("plan: parse: %w", err)
	}
	return p, nil
}

// ParseRiskPolicy decodes a risk policy document, rejecting unknown fields.
// It also validates that default_tier and every rule's tier are known tier
// strings (§tierOrder) — a policy with an illegal tier fails loud here,
// at load time, rather than surfacing later as a task-level "minimum_risk_tier
// does not match recomputed value" error that misdirects diagnosis toward
// the plan instead of the broken policy file.
func ParseRiskPolicy(b []byte) (RiskPolicy, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var pol RiskPolicy
	if err := dec.Decode(&pol); err != nil {
		return RiskPolicy{}, fmt.Errorf("plan: parse risk policy: %w", err)
	}
	if _, ok := tierOrder[pol.DefaultTier]; !ok {
		return RiskPolicy{}, fmt.Errorf("plan: parse risk policy: unknown default_tier %q", pol.DefaultTier)
	}
	for i, r := range pol.Rules {
		if _, ok := tierOrder[r.Tier]; !ok {
			return RiskPolicy{}, fmt.Errorf("plan: parse risk policy: rule %d: unknown tier %q", i, r.Tier)
		}
	}
	return pol, nil
}
