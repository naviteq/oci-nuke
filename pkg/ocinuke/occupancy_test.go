package ocinuke_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/ekristen/libnuke/pkg/queue"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// keyedResource is the least a queue item needs here: Remove, and the UniqueKey the entries name.
type keyedResource struct{ id string }

func (keyedResource) Remove(context.Context) error { return nil }

func (k keyedResource) UniqueKey() string { return k.id }

func item(typ, id string, state queue.ItemState, reason string) *queue.Item {
	return &queue.Item{Type: typ, State: state, Reason: reason, Resource: keyedResource{id}}
}

func TestCompartmentOccupancy(t *testing.T) {
	const c, other = "ocid1.compartment.oc1..c", "ocid1.compartment.oc1..other"
	const bucket, vault, pending = "Bucket", "Vault", "vault already scheduled for deletion"

	items := []*queue.Item{
		item("Compartment", c, queue.ItemStateNew, ""),
		item("Subnet", "s-new", queue.ItemStateNew, ""),
		item("Vcn", "v-dep", queue.ItemStateNewDependency, ""),
		item("Instance", "i-wait", queue.ItemStateWaiting, ""),
		item(bucket, "b-hold", queue.ItemStateHold, "HTTP 409"),
		item(vault, "vault-done", queue.ItemStateFinished, ""),
		item("BootVolume", "bv-dead", queue.ItemStateFiltered, "BootVolume is TERMINATED, not available"),
		item("ObjectVersion", "ov-kept", queue.ItemStateFiltered, "filtered by config"),
		item("Cluster", "k-failed", queue.ItemStateFailed, "boom"),
	}
	skips := []scope.SkipEvent{
		{Reason: scope.ReasonScheduledDeletion, ResourceType: vault, ResourceID: "vault-done", CompartmentID: c, Detail: pending},
		{Reason: scope.ReasonScheduledDeletion, ResourceType: vault, ResourceID: "vault-done", CompartmentID: c, Detail: pending},
		{Reason: scope.ReasonProtectedByTag, ResourceType: bucket, ResourceID: "b-kept", CompartmentID: c},
		{Reason: scope.ReasonProtectedByTag, ResourceType: bucket, ResourceID: "elsewhere", CompartmentID: other},
		{Reason: scope.ReasonCompartmentNotActive, ResourceType: "Compartment", ResourceID: "child", CompartmentID: c},
		{Reason: scope.ReasonAPIError, ResourceType: "Instance", ResourceID: "i-x", CompartmentID: c},
	}

	got := ocinuke.CompartmentOccupancy(c, items, skips)

	want := ocinuke.Occupancy{
		InFlight: []string{"Subnet s-new", "Vcn v-dep", "Instance i-wait", "Bucket b-hold"},
		Residue: []string{
			"ObjectVersion ov-kept (filtered by config)",
			"Cluster k-failed (delete failed: boom)",
			"Vault vault-done (scheduled-deletion: vault already scheduled for deletion)",
			"Bucket b-kept (protected-by-tag)",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CompartmentOccupancy() =\n  %#v\nwant\n  %#v", got, want)
	}
}
