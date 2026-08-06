package contract

import "testing"

func TestEventZeroValueIsInvalid(t *testing.T) {
	var e Event
	if e.Valid() {
		t.Fatal("zero event must be invalid")
	}
}

func TestEventValidRequiresProviderAndKind(t *testing.T) {
	e := Event{Provider: ProviderClaude, Kind: KindDelta, Raw: []byte("{}")}
	if !e.Valid() {
		t.Fatal("provider+kind+raw must be valid")
	}
	if (Event{Provider: "x", Kind: KindDelta, Raw: []byte("{}")}).Valid() {
		t.Fatal("unknown provider must be invalid")
	}
	if (Event{Provider: ProviderCodex, Kind: KindMalformed, Raw: []byte("{}")}).Valid() {
		t.Fatal("malformed without Err must be invalid")
	}
}
