package evidence

// Mutation is a single append-only journal entry recording that a task
// applied a content mutation: the CAS digest identifies the mutated
// content (e.g. a patch) so the journal itself stays small while the
// content it references lives in the CAS store (see cas.go).
type Mutation struct {
	MutationID string `json:"mutation_id"` // ULID
	TaskRef    string `json:"task_ref"`    // "P1/T1"
	Digest     string `json:"digest"`      // patch 內容 CAS digest
	CreatedAt  string `json:"created_at"`
}
