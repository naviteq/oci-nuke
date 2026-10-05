package ocinuke

import (
	"github.com/ekristen/libnuke/pkg/queue"
	"github.com/ekristen/libnuke/pkg/resource"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

// compartmentTypeName is resources.CompartmentResourceType, spelled out because this package
// cannot import resources.
const compartmentTypeName = "Compartment"

// filteredByConfig is the item reason libnuke sets when a config filter matched. A resource its
// own Filter() excluded carries that error's text instead, and is usually already gone.
const filteredByConfig = "filtered by config"

// Occupancy is what a run knows still sits in one compartment. InFlight is being removed by this
// run and is worth waiting for; Residue will outlive the run, so deleting the compartment now
// can only fail. Each entry reads "<Type> <id>", with the reason in brackets for Residue.
type Occupancy struct {
	InFlight []string
	Residue  []string
}

// CompartmentOccupancy builds the Occupancy of compartmentID from the compartment's own queue
// and every skip event reported so far. The Compartment item itself and other compartments'
// events are ignored; a child compartment is not visible here and is the caller's to check.
func CompartmentOccupancy(compartmentID string, items []*queue.Item, skips []scope.SkipEvent) Occupancy {
	var occ Occupancy
	seen := map[string]bool{}
	add := func(list *[]string, entry string) {
		if !seen[entry] {
			seen[entry] = true
			*list = append(*list, entry)
		}
	}

	for _, item := range items {
		if item.Type == compartmentTypeName {
			continue
		}
		name := item.Type + " " + itemID(item)
		switch item.GetState() {
		case queue.ItemStateNew, queue.ItemStateNewDependency,
			queue.ItemStatePending, queue.ItemStatePendingDependency,
			queue.ItemStateWaiting, queue.ItemStateHold:
			add(&occ.InFlight, name)
		case queue.ItemStateFailed:
			add(&occ.Residue, name+" (delete failed: "+item.GetReason()+")")
		case queue.ItemStateFiltered:
			if item.GetReason() == filteredByConfig {
				add(&occ.Residue, name+" ("+filteredByConfig+")")
			}
		}
	}

	for i := range skips {
		evt := &skips[i]
		if evt.CompartmentID != compartmentID || evt.ResourceType == compartmentTypeName || !evt.Reason.OutlivesRun() {
			continue
		}
		entry := evt.ResourceType + " " + evt.ResourceID + " (" + string(evt.Reason)
		if evt.Detail != "" {
			entry += ": " + evt.Detail
		}
		add(&occ.Residue, entry+")")
	}

	return occ
}

func itemID(item *queue.Item) string {
	if getter, ok := item.Resource.(resource.UniqueKeyGetter); ok {
		return getter.UniqueKey()
	}
	return ""
}
