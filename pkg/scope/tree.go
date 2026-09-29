package scope

import (
	"context"
	"fmt"
	"sort"

	"github.com/oracle/oci-go-sdk/v65/identity"
)

// IdentityClient is the narrow slice of identity.IdentityClient the resolver needs -- a
// hand-written interface so tests can fake it with zero network, mirroring the seam
// ARCHITECTURE.md recommends for pkg/ociauth.
type IdentityClient interface {
	ListCompartments(ctx context.Context, req identity.ListCompartmentsRequest) (identity.ListCompartmentsResponse, error)
	GetCompartment(ctx context.Context, req identity.GetCompartmentRequest) (identity.GetCompartmentResponse, error)
}

// FetchTenancyTree issues the ONE Identity API call the whole resolver needs.
// CompartmentIdInSubtree is only legal when CompartmentId is the tenancy root -- live-verified
// against a real tenancy, 2026-08-07: a non-root CompartmentId with CompartmentIdInSubtree=true
// returns HTTP 400 InvalidParameter. tenancyOCID must always be the tenancy root, never the
// operator's --compartment-id target; the full subtree is then resolved in-memory by Resolve.
// Every lifecycle state is returned by this call -- callers filter client-side (Resolve does
// this for the ACTIVE-only default).
func FetchTenancyTree(ctx context.Context, client IdentityClient, tenancyOCID string) ([]identity.Compartment, error) {
	subtree := true
	req := identity.ListCompartmentsRequest{
		CompartmentId:          &tenancyOCID,
		CompartmentIdInSubtree: &subtree,
		AccessLevel:            identity.ListCompartmentsAccessLevelAny,
	}

	var all []identity.Compartment
	for {
		resp, err := client.ListCompartments(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing tenancy compartment tree: %w", err)
		}
		all = append(all, resp.Items...)
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return all, nil
}

// Tree is a pure, dependency-free in-memory representation of a tenancy's compartment
// hierarchy, built from FetchTenancyTree's flat response. This is what makes SCOPE-02's
// "prune during traversal, not post-filter" property testable against a synthetic fixture with
// zero mocking.
type Tree struct {
	byID       map[string]identity.Compartment
	childrenOf map[string][]string
}

// BuildTree indexes a flat compartment list by OCID and by parent OCID. The tenancy root is
// never itself an item in compartments (FetchTenancyTree's live-verified response never
// includes it), so the root is never a key in byID -- this is what lets AncestorBlocklisted's
// parent-chain walk terminate without an explicit root sentinel.
func BuildTree(compartments []identity.Compartment) *Tree {
	t := &Tree{byID: map[string]identity.Compartment{}, childrenOf: map[string][]string{}}
	for _, c := range compartments {
		t.byID[*c.Id] = c
		if c.CompartmentId != nil {
			t.childrenOf[*c.CompartmentId] = append(t.childrenOf[*c.CompartmentId], *c.Id)
		}
	}
	return t
}

// Name returns the display name recorded for id in the fetched tree, and whether id was found at
// all. This is the seam Plan 02-05's confirmation prompt reads the live target display name
// through, rather than issuing a second GetCompartment call for data FetchTenancyTree's single
// root-scoped call already retrieved -- byID itself stays unexported so callers cannot reach
// into the full identity.Compartment record and grow a dependency on its shape.
func (t *Tree) Name(id string) (string, bool) {
	c, ok := t.byID[id]
	if !ok || c.Name == nil {
		return "", false
	}
	return *c.Name, true
}

// Exists reports whether id is a key in the fetched tree's byID map. Callers (Plan 02-04's
// runPipeline) MUST check this before trusting Resolve's output: an unknown or mistyped target
// OCID absent from byID would otherwise walk as if it were a real, childless compartment
// (silently producing a single-node in-scope set) instead of surfacing as an error.
func (t *Tree) Exists(id string) bool {
	_, ok := t.byID[id]
	return ok
}

// LifecycleStateActive is identity.CompartmentLifecycleStateActive as a plain string, so a
// caller comparing against LifecycleState's return value does not have to import the OCI SDK's
// identity package for one constant.
const LifecycleStateActive = string(identity.CompartmentLifecycleStateActive)

// LifecycleState returns the lifecycleState recorded for id, and whether id was found at all.
// Resolve prunes every non-ACTIVE node silently by design, so a caller that needs to tell "this
// compartment was scanned and was clean" from "this compartment was never looked at" has to ask
// separately -- and a caller handed an empty in-scope set for a target that Exists reports as
// present has no other way to find out why (NR-787).
func (t *Tree) LifecycleState(id string) (string, bool) {
	c, ok := t.byID[id]
	if !ok {
		return "", false
	}
	return string(c.LifecycleState), true
}

// NonActiveCompartment is a node Resolve pruned for not being ACTIVE, carrying the state it was
// in -- the state is the whole point: DELETING and CREATING call for different operator actions.
type NonActiveCompartment struct {
	ID             string
	LifecycleState string
}

// Resolve walks the tree from target, breadth-first via recursion, returning the set of in-scope
// compartment OCIDs (ACTIVE nodes only), the list of blocklisted OCIDs encountered directly under
// the walk, and the non-ACTIVE nodes pruned the same way (in both cases their descendants are
// never visited, so never appear here either).
//
// The two prunes are returned separately because they are different findings, not because the
// walk treats them differently: a blocklisted subtree was deliberately excluded by
// configuration, a non-ACTIVE one could not be examined.
func (t *Tree) Resolve(target string, blocklist map[string]struct{}) (
	inScope map[string]struct{}, skippedBlocklisted []string, skippedNonActive []NonActiveCompartment,
) {
	inScope = map[string]struct{}{}
	var walk func(id string)
	walk = func(id string) {
		// SCOPE-02: the prune happens HERE, while the in-scope set is being built during the
		// walk itself -- it is NOT a filter applied to an already-fully-built set afterwards.
		// The blocklisted node is recorded as skipped and returned WITHOUT ever being written
		// into inScope, and its children are never appended to the walk -- they are never
		// visited, never recorded, anywhere. A refactor that first materializes the full
		// descendant set and filters it afterward would look identical on a passing test today
		// and would silently reintroduce the exact enumeration this function exists to prevent
		// for anything downstream that touches the intermediate set. Do not restructure this
		// into a post-filter. See 02-CONTEXT.md's "traversal design, decided" addendum.
		if _, blocked := blocklist[id]; blocked {
			skippedBlocklisted = append(skippedBlocklisted, id)
			return
		}
		if c, ok := t.byID[id]; ok && c.LifecycleState != identity.CompartmentLifecycleStateActive {
			// Non-ACTIVE: excluded from the in-scope set, and its children are never descended
			// into either (Assumption A2: ACTIVE-only default) -- but recorded, which it was not
			// before NR-787. The prune was the only silent one in this walk.
			skippedNonActive = append(skippedNonActive, NonActiveCompartment{
				ID:             id,
				LifecycleState: string(c.LifecycleState),
			})
			return
		}
		inScope[id] = struct{}{}
		for _, child := range t.childrenOf[id] {
			walk(child)
		}
	}
	walk(target)
	return inScope, skippedBlocklisted, skippedNonActive
}

// AncestorBlocklisted walks the parent chain starting at target (checking target itself first,
// then each ancestor in turn) and reports the first blocklisted OCID found. Resolve alone only
// looks downward from target and cannot catch "target nested under a blocklisted ancestor" --
// this is the independent check for that case (T-02-03). The walk terminates when a node's
// OCID is absent from byID (the tenancy root, which is never itself an item in the fetched
// set), so it always completes even against a tree with many levels above target.
func (t *Tree) AncestorBlocklisted(target string, blocklist map[string]struct{}) (blockedOCID string, blocked bool) {
	id := target
	for {
		if _, isBlocked := blocklist[id]; isBlocked {
			return id, true
		}
		c, ok := t.byID[id]
		if !ok || c.CompartmentId == nil {
			return "", false
		}
		id = *c.CompartmentId
	}
}

// DeletionOrder returns inScope's members ordered deepest-first: children strictly before every
// ancestor of theirs that is also a member of inScope, with same-depth siblings tie-broken by
// ascending OCID for determinism. This is Phase 6's fix for buildNukes' non-deterministic `for
// compartmentID := range inScope` map iteration (06-RESEARCH.md "The runNukes structural
// question") -- once *libnuke.Nuke construction follows this order, runNukes' existing sequential
// `for _, n := range nukes { n.Run(ctx) }` loop guarantees no parent compartment's Nuke is even
// constructed until every compartment nested beneath it has already run to completion.
//
// Depth is computed the same way AncestorBlocklisted's parent-chain walk works: starting at id,
// repeatedly step to CompartmentId while it remains a key in t.byID, counting steps. Every
// id in inScope terminates the moment its parent chain climbs above the fetched tree (the
// tenancy root, which -- per BuildTree's own doc comment -- is never itself a key in byID), so a
// member of inScope nested arbitrarily far under the root is still ordered correctly relative to
// its own descendants: an id's depth this way is always strictly greater than every one of its
// ancestors' depths, and strictly less than every one of its descendants' depths.
//
// An empty inScope returns an empty, non-nil slice; DeletionOrder never panics on an empty or
// nil input.
func (t *Tree) DeletionOrder(inScope map[string]struct{}) []string {
	ids := make([]string, 0, len(inScope))
	depths := make(map[string]int, len(inScope))
	for id := range inScope {
		ids = append(ids, id)
		depths[id] = t.depth(id)
	}

	sort.Slice(ids, func(i, j int) bool {
		if depths[ids[i]] != depths[ids[j]] {
			return depths[ids[i]] > depths[ids[j]] // deeper (larger depth) sorts first
		}
		return ids[i] < ids[j] // same-depth siblings: ascending OCID
	})

	return ids
}

// depth counts the number of parent-chain hops from id until the chain leaves t.byID (the
// tenancy root, or any other ancestor absent from the fetched tree). Mirrors
// AncestorBlocklisted's loop shape exactly, replacing the blocklist check with a step counter.
func (t *Tree) depth(id string) int {
	depth := 0
	cur := id
	for {
		c, ok := t.byID[cur]
		if !ok || c.CompartmentId == nil {
			return depth
		}
		cur = *c.CompartmentId
		depth++
	}
}

// HasBlocklistedDescendant reports whether any descendant of id -- a direct child, or a
// descendant arbitrarily many levels down -- is a member of blocklist. id itself is deliberately
// never checked: callers needing "is id itself blocklisted" already have that answer from
// Resolve or AncestorBlocklisted, and conflating the two here would make this function's result
// ambiguous about which node was actually found. Mirrors Resolve's downward recursive walk
// shape, but walks t.childrenOf directly rather than re-deriving in-scope membership.
func (t *Tree) HasBlocklistedDescendant(id string, blocklist map[string]struct{}) bool {
	for _, child := range t.childrenOf[id] {
		if _, blocked := blocklist[child]; blocked {
			return true
		}
		if t.HasBlocklistedDescendant(child, blocklist) {
			return true
		}
	}
	return false
}
