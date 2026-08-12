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
func ParseRiskPolicy(b []byte) (RiskPolicy, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var pol RiskPolicy
	if err := dec.Decode(&pol); err != nil {
		return RiskPolicy{}, fmt.Errorf("plan: parse risk policy: %w", err)
	}
	return pol, nil
}
