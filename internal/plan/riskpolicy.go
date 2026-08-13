package plan

// intersects reports whether a and b share at least one element.
func intersects(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, x := range a {
		set[x] = true
	}
	for _, y := range b {
		if set[y] {
			return true
		}
	}
	return false
}

// ComputeMinimum returns the minimum risk tier for t under p: the highest
// tier among every rule whose match contexts or modules intersect t's
// impact, or p.DefaultTier when no rule matches at all.
func (p RiskPolicy) ComputeMinimum(t Task) string {
	matched := false
	best := ""
	bestRank := 0
	for _, r := range p.Rules {
		if !intersects(r.Match.Contexts, t.Impact.Contexts) && !intersects(r.Match.Modules, t.Impact.Modules) {
			continue
		}
		matched = true
		if rank, ok := tierOrder[r.Tier]; ok && rank > bestRank {
			best = r.Tier
			bestRank = rank
		}
	}
	if !matched {
		return p.DefaultTier
	}
	return best
}
