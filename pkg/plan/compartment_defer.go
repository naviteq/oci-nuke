package plan

import (
	"sort"
	"strings"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

// scheduledDeletionTypes are the types a run schedules for deletion rather than deletes: Vault,
// KmsKey and VaultSecret, by their registry names. Once scheduled they stay in the compartment
// for days, so a compartment holding one cannot be deleted by the run that schedules it.
var scheduledDeletionTypes = map[string]bool{"Vault": true, "KmsKey": true, "VaultSecret": true}

// filteredByConfigDetail is the item reason libnuke gives a resource a config filter kept.
const filteredByConfigDetail = "filtered by config"

// DeferNonEmptyCompartments turns a would-remove Compartment entry into a skipped
// compartment-not-empty one when the plan itself shows the compartment will not be empty after
// the run: something in it outlives the run (OutlivesRun skip reasons, a config filter, a type
// that is only scheduled for deletion), or a child compartment stays. Compartment.Remove() makes
// the same call at apply time; this makes the plan say so before anyone approves it.
//
// children and lifecycleState come from the run's compartment tree. A child that is DELETED never
// blocks; any other child blocks unless its own entry is would-remove or removed and it is not
// deferred itself, so a deferral propagates up the tree.
func DeferNonEmptyCompartments(
	entries []Entry,
	children func(id string) []string,
	lifecycleState func(id string) (string, bool),
) []Entry {
	compartments := map[string]int{}
	contents := map[string][]int{}
	for i := range entries {
		if entries[i].ResourceType == compartmentEntryResourceType {
			compartments[entries[i].ResourceID] = i
			continue
		}
		contents[entries[i].CompartmentID] = append(contents[entries[i].CompartmentID], i)
	}

	ids := make([]string, 0, len(compartments))
	for id := range compartments {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// First settle which compartments stay, so each one's detail below names its final blockers
	// rather than whatever had been decided when it happened to be visited.
	stays := map[string]bool{}
	blockersOf := func(id string) []string {
		blockers := residueIn(entries, contents[id])
		return append(blockers, stayingChildren(entries, compartments, stays, id, children, lifecycleState)...)
	}
	for changed := true; changed; {
		changed = false
		for _, id := range ids {
			if !stays[id] && entries[compartments[id]].State == StateWouldRemove && len(blockersOf(id)) > 0 {
				stays[id] = true
				changed = true
			}
		}
	}

	for _, id := range ids {
		if !stays[id] {
			continue
		}
		entry := &entries[compartments[id]]
		entry.State = StateSkipped
		entry.Reason = scope.ReasonCompartmentNotEmpty
		entry.Detail = "will still hold " + strings.Join(blockersOf(id), "; ") + "; a later run deletes it once they are gone"
	}
	return entries
}

func residueIn(entries []Entry, indexes []int) []string {
	var out []string
	for _, i := range indexes {
		e := &entries[i]
		name := e.ResourceType + " " + e.ResourceID
		switch {
		case (e.State == StateSkipped || e.State == StateLeftover) && e.Reason.OutlivesRun():
			out = append(out, name+" ("+string(e.Reason)+")")
		case e.State == StateFiltered && e.Detail == filteredByConfigDetail:
			out = append(out, name+" ("+filteredByConfigDetail+")")
		case e.State == StateWouldRemove && scheduledDeletionTypes[e.ResourceType]:
			out = append(out, name+" (scheduled for deletion, not deleted, by this run)")
		}
	}
	return out
}

func stayingChildren(
	entries []Entry, compartments map[string]int, stays map[string]bool, id string,
	children func(string) []string, lifecycleState func(string) (string, bool),
) []string {
	var out []string
	for _, child := range children(id) {
		if state, ok := lifecycleState(child); ok && state == "DELETED" {
			continue
		}
		if i, ok := compartments[child]; ok && !stays[child] &&
			(entries[i].State == StateWouldRemove || entries[i].State == StateRemoved) {
			continue
		}
		out = append(out, compartmentEntryResourceType+" "+child)
	}
	return out
}
