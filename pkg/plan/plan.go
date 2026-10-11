// Package plan provides the single, shared data model and writer both the plan artifact
// (PLAN-01) and the leftover report (PLAN-03) render from. libnuke.Nuke.run (the removal loop)
// is unexported and reachable only through the monolithic Nuke.Run; the only externally
// observable state is n.Queue.GetItems() after Run returns. That constraint is why the plan
// artifact and the leftover report are the same writer over the same walk, differing only in
// which ItemStates are "of interest" -- see 03-RESEARCH.md Q4/Q5/Q6.
package plan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

// EntryState is the complete, single vocabulary both RenderTable and the JSON body use verbatim
// -- there is no separate display-label mapping anywhere in the codebase. Two vocabularies that
// drift is exactly the failure this phase exists to prevent, one level up (03-CONTEXT.md
// orchestrator decision 2).
type EntryState string

const (
	// StateWouldRemove means a dry run scanned this resource and would delete it.
	StateWouldRemove EntryState = "would-remove"
	// StateRemoved means a destructive run successfully removed this resource.
	StateRemoved EntryState = "removed"
	// StateFiltered means a configured filter excluded this resource from the run entirely.
	StateFiltered EntryState = "filtered"
	// StateLeftover means a destructive run finished and this resource is still present.
	StateLeftover EntryState = "leftover"
	// StateSkipped means this resource never became a candidate at all -- out-of-scope,
	// blocklisted, or another scope.SkipEvent-carried reason, merged in via MergeSkipEvents.
	StateSkipped EntryState = "skipped"
)

// SchemaVersion is PLAN-02's versioned schema, bumped only on a breaking field change to Entry
// or Body.
const SchemaVersion = 1

// Entry is one resource's outcome, whether a plan candidate, a completed removal, a leftover, or
// a skip. Every field carries a JSON tag matching its Go name in snake_case; State/ResourceType/
// ResourceID/CompartmentID/Region are never omitempty, so every entry has a stable, predictable
// field set regardless of which fields happen to be empty for that entry.
type Entry struct {
	ResourceType  string              `json:"resource_type"`
	ResourceID    string              `json:"resource_id"`
	CompartmentID string              `json:"compartment_id"`
	Region        string              `json:"region"`
	State         EntryState          `json:"state"`
	Reason        scope.RefusalReason `json:"reason,omitempty"`
	Detail        string              `json:"detail,omitempty"`
}

// Body is the hashed content of a plan or leftover artifact.
type Body struct {
	SchemaVersion int     `json:"schema_version"`
	Entries       []Entry `json:"entries"`
}

// Artifact is a Body plus metadata about its own generation. GeneratedAt is metadata only and is
// never part of the hashed bytes -- two artifacts produced from the same logical Body at
// different times must still hash identically.
type Artifact struct {
	Body        Body      `json:"body"`
	Hash        string    `json:"hash"`
	GeneratedAt time.Time `json:"generated_at"`
}

// Sort applies the single explicit composite tiebreak used everywhere this package resolves
// nondeterministic order (a slice built by ranging a map, or libnuke's concurrent
// scanner-to-channel fan-out) into a stable sequence: region, then resource type, then
// compartment ID, then resource ID. Idempotent -- safe to call more than once on the same Body.
func (b *Body) Sort() {
	sort.Slice(b.Entries, func(i, j int) bool {
		a, c := b.Entries[i], b.Entries[j]
		if a.Region != c.Region {
			return a.Region < c.Region
		}
		if a.ResourceType != c.ResourceType {
			return a.ResourceType < c.ResourceType
		}
		if a.CompartmentID != c.CompartmentID {
			return a.CompartmentID < c.CompartmentID
		}
		return a.ResourceID < c.ResourceID
	})
}

// CanonicalJSON encodes body deterministically: entries are copied and sorted first (the
// caller's own slice order is never mutated as a side effect it didn't ask for), then encoded
// via a json.Encoder with SetEscapeHTML(false) -- the one, consistently applied encoding path.
// Hash encodes DecisionBody(body) through this same function, so the hashed bytes are exactly the
// bytes that body's decision set would be written as.
func CanonicalJSON(body Body) ([]byte, error) {
	sorted := Body{
		SchemaVersion: body.SchemaVersion,
		Entries:       append([]Entry(nil), body.Entries...),
	}
	sorted.Sort()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(sorted); err != nil {
		return nil, fmt.Errorf("encoding canonical JSON: %w", err)
	}
	return buf.Bytes(), nil
}

// DecisionBody returns the part of body an approval authorizes: every entry except the ones the
// run leaves alone by construction -- filtered entries, and skipped entries other than a
// compartment deferred as not empty -- with Detail cleared. See ADR-0004.
//
// The excluded entries are not decisions. A too-young resource crossing its min-age threshold, a
// TERMINATED volume dropping out of a listing, or a free-text Detail are all outside what the
// operator approves, and hashing them made every approval in a live sandbox race the tenancy. A
// deferred compartment stays in because libnuke still calls Remove() on it; only its Detail, the
// blocker prose, is dropped.
//
// The exclusion list is deliberately negative: a state added later is hashed until someone
// decides otherwise, so forgetting to classify it fails closed (a spurious mismatch), never open.
// Any entry that leaves the excluded states -- a skipped resource that becomes would-remove, a
// filter dropped from the config -- enters the decision set and changes the hash.
func DecisionBody(body Body) Body {
	decisions := Body{SchemaVersion: body.SchemaVersion, Entries: []Entry{}}
	for i := range body.Entries {
		e := body.Entries[i]
		if e.State == StateFiltered ||
			(e.State == StateSkipped && e.Reason != scope.ReasonCompartmentNotEmpty) {
			continue
		}
		e.Detail = ""
		decisions.Entries = append(decisions.Entries, e)
	}
	return decisions
}

// Hash returns the hex-encoded SHA-256 sum of the canonical JSON of body's decision set
// (DecisionBody) -- the plan hash an apply run's rescan must reproduce before it deletes anything.
func Hash(body Body) (string, error) {
	b, err := CanonicalJSON(DecisionBody(body))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// NewArtifact sorts a copy of body's entries, computes the hash over that sorted Body via Hash,
// and stamps GeneratedAt as time.Now().UTC() -- the UTC conversion itself strips the monotonic
// reading, per 03-RESEARCH.md's Q4 time-formatting note. GeneratedAt is never part of the hashed
// bytes.
func NewArtifact(body Body) (Artifact, error) {
	sorted := Body{
		SchemaVersion: body.SchemaVersion,
		Entries:       append([]Entry(nil), body.Entries...),
	}
	sorted.Sort()

	hash, err := Hash(sorted)
	if err != nil {
		return Artifact{}, fmt.Errorf("computing artifact hash: %w", err)
	}

	return Artifact{
		Body:        sorted,
		Hash:        hash,
		GeneratedAt: time.Now().UTC(),
	}, nil
}
