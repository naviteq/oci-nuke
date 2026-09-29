package ocinuke

import (
	"context"
	"strings"
	"testing"

	"github.com/ekristen/libnuke/pkg/filter"
	"github.com/ekristen/libnuke/pkg/queue"
	"github.com/ekristen/libnuke/pkg/types"
)

// fakePropertyResource implements resource.PropertyGetter (Remove + Properties()) so
// queue.Item.GetProperty can resolve a value through it -- the normal path DetectUnmatchedFilters
// exercises for every item whose resource supports custom properties.
type fakePropertyResource struct {
	props types.Properties
}

func (r *fakePropertyResource) Remove(_ context.Context) error { return nil }
func (r *fakePropertyResource) Properties() types.Properties   { return r.props }

// fakeNoPropertyResource implements only resource.Resource, deliberately NOT
// resource.PropertyGetter -- queue.Item.GetProperty(key) with a non-empty key returns a non-nil
// error for this type ("does not support custom properties"), exercising the same
// "unable to get property" tolerance libnuke's own filterWithoutGroups relies on.
type fakeNoPropertyResource struct{}

func (r *fakeNoPropertyResource) Remove(_ context.Context) error { return nil }

func newPropertyItem(resourceType string, props map[string]string) *queue.Item {
	p := types.NewProperties()
	for k, v := range props {
		p.Set(k, v)
	}
	return &queue.Item{
		Type:     resourceType,
		Resource: &fakePropertyResource{props: p},
	}
}

func newNoPropertyItem(resourceType string) *queue.Item {
	return &queue.Item{
		Type:     resourceType,
		Resource: &fakeNoPropertyResource{},
	}
}

const (
	filterTestTypeA  = "TypeA"
	filterTestTypeB  = "TypeB"
	filterTestProp   = "Name" // property name every fixture filter is configured against
	filterTestValue  = "foo"  // the value every fixture filter looks for
	filterTestOther  = "bar"  // a value that does NOT match filterTestValue
	filterTestEnvKey = "Env"  // a second property name, used for the __global__ filter case
	filterTestEnvHit = "prod" // matches the __global__ filter's configured value
	filterTestEnvOff = "dev"  // does not match the __global__ filter's configured value
)

// TestDetectUnmatchedFilters_FilterMatchingNothingWarns proves the core SAFE-08 case: a filter
// configured for a resource type is checked against every scanned item of that type, and if none
// of them match, exactly one FilterWarning is produced naming that type and filter.
func TestDetectUnmatchedFilters_FilterMatchingNothingWarns(t *testing.T) {
	items := []*queue.Item{
		newPropertyItem(filterTestTypeA, map[string]string{filterTestProp: filterTestOther}),
	}
	nukeFilters := filter.Filters{
		filterTestTypeA: {{Property: filterTestProp, Type: filter.Exact, Value: filterTestValue}},
	}

	warnings := DetectUnmatchedFilters(nukeFilters, items)

	if len(warnings) != 1 {
		t.Fatalf("DetectUnmatchedFilters() returned %d warnings, want 1: %+v", len(warnings), warnings)
	}
	if warnings[0].ResourceType != filterTestTypeA {
		t.Errorf("warnings[0].ResourceType = %q, want %q", warnings[0].ResourceType, filterTestTypeA)
	}
	if warnings[0].Filter.Property != filterTestProp {
		t.Errorf("warnings[0].Filter.Property = %q, want %q", warnings[0].Filter.Property, filterTestProp)
	}
}

// TestDetectUnmatchedFilters_FilterMatchingAtLeastOneDoesNotWarn proves the inverse: the same
// filter shape, but one item's property does match, produces zero warnings for that filter.
func TestDetectUnmatchedFilters_FilterMatchingAtLeastOneDoesNotWarn(t *testing.T) {
	items := []*queue.Item{
		newPropertyItem(filterTestTypeA, map[string]string{filterTestProp: filterTestOther}),
		newPropertyItem(filterTestTypeA, map[string]string{filterTestProp: filterTestValue}),
	}
	nukeFilters := filter.Filters{
		filterTestTypeA: {{Property: filterTestProp, Type: filter.Exact, Value: filterTestValue}},
	}

	warnings := DetectUnmatchedFilters(nukeFilters, items)

	if len(warnings) != 0 {
		t.Fatalf("DetectUnmatchedFilters() returned %d warnings, want 0: %+v", len(warnings), warnings)
	}
}

// TestDetectUnmatchedFilters_GlobalFilterCheckedPerObservedType proves a __global__ filter is
// evaluated independently per concrete resource type, not as a single tenancy-wide pass/fail --
// it matches items of type A but not type B, so only B should warn.
func TestDetectUnmatchedFilters_GlobalFilterCheckedPerObservedType(t *testing.T) {
	items := []*queue.Item{
		newPropertyItem(filterTestTypeA, map[string]string{filterTestEnvKey: filterTestEnvHit}),
		newPropertyItem(filterTestTypeB, map[string]string{filterTestEnvKey: filterTestEnvOff}),
	}
	nukeFilters := filter.Filters{
		filter.Global: {{Property: filterTestEnvKey, Type: filter.Exact, Value: filterTestEnvHit}},
	}

	warnings := DetectUnmatchedFilters(nukeFilters, items)

	if len(warnings) != 1 {
		t.Fatalf("DetectUnmatchedFilters() returned %d warnings, want 1: %+v", len(warnings), warnings)
	}
	if warnings[0].ResourceType != filterTestTypeB {
		t.Errorf("warnings[0].ResourceType = %q, want %q (type A's __global__ filter matched, must not warn)",
			warnings[0].ResourceType, filterTestTypeB)
	}
}

// TestDetectUnmatchedFilters_InvertHonored proves Invert is honored exactly as
// filterWithoutGroups does: every item's property exactly equals the filter's Value, so the
// UN-inverted match is true for all of them -- but Invert:true means none of them actually
// satisfy the filter, so a warning must still be produced. A buggy implementation that ignored
// Invert (checking the raw match result instead of `match != Invert`) would wrongly report zero
// warnings here.
func TestDetectUnmatchedFilters_InvertHonored(t *testing.T) {
	items := []*queue.Item{
		newPropertyItem(filterTestTypeA, map[string]string{filterTestProp: filterTestValue}),
		newPropertyItem(filterTestTypeA, map[string]string{filterTestProp: filterTestValue}),
	}
	nukeFilters := filter.Filters{
		filterTestTypeA: {{Property: filterTestProp, Type: filter.Exact, Value: filterTestValue, Invert: true}},
	}

	warnings := DetectUnmatchedFilters(nukeFilters, items)

	if len(warnings) != 1 {
		t.Fatalf("DetectUnmatchedFilters() returned %d warnings, want 1 (Invert must flip every "+
			"item's match to non-match): %+v", len(warnings), warnings)
	}
}

// TestDetectUnmatchedFilters_PropertyGetErrorTolerated proves an item whose GetProperty errors
// (its Resource does not implement resource.PropertyGetter) does not panic or abort detection --
// remaining items are still correctly evaluated, exactly the "unable to get property" tolerance
// libnuke's own filterWithoutGroups relies on.
func TestDetectUnmatchedFilters_PropertyGetErrorTolerated(t *testing.T) {
	items := []*queue.Item{
		newNoPropertyItem(filterTestTypeA),
		newPropertyItem(filterTestTypeA, map[string]string{filterTestProp: filterTestValue}),
	}
	nukeFilters := filter.Filters{
		filterTestTypeA: {{Property: filterTestProp, Type: filter.Exact, Value: filterTestValue}},
	}

	warnings := DetectUnmatchedFilters(nukeFilters, items)

	if len(warnings) != 0 {
		t.Fatalf("DetectUnmatchedFilters() returned %d warnings, want 0 -- the second item's match "+
			"must still be evaluated despite the first item's GetProperty error: %+v", len(warnings), warnings)
	}
}

// TestDetectUnmatchedFilters_FilterForNeverScannedTypeWarns proves a filter configured for a
// resource type absent from items entirely still produces a warning -- it must not be silently
// skipped for lack of an entry in the type-grouping map.
func TestDetectUnmatchedFilters_FilterForNeverScannedTypeWarns(t *testing.T) {
	const neverScannedType = "NeverScannedType"
	items := []*queue.Item{
		newPropertyItem(filterTestTypeA, map[string]string{filterTestProp: filterTestValue}),
	}
	nukeFilters := filter.Filters{
		neverScannedType: {{Property: filterTestProp, Type: filter.Exact, Value: filterTestValue}},
	}

	warnings := DetectUnmatchedFilters(nukeFilters, items)

	if len(warnings) != 1 {
		t.Fatalf("DetectUnmatchedFilters() returned %d warnings, want 1: %+v", len(warnings), warnings)
	}
	if warnings[0].ResourceType != neverScannedType {
		t.Errorf("warnings[0].ResourceType = %q, want %q", warnings[0].ResourceType, neverScannedType)
	}
}

// TestFormatWarnings_ProducesOneLinePerWarning proves FormatWarnings renders a human-readable
// line per warning, naming the resource type, filter property, type and value -- and that it is
// phrased as a warning, not an error.
func TestFormatWarnings_ProducesOneLinePerWarning(t *testing.T) {
	warnings := []FilterWarning{
		{ResourceType: filterTestTypeA, Filter: filter.Filter{Property: filterTestProp, Type: filter.Exact, Value: filterTestValue}},
	}

	lines := FormatWarnings(warnings)

	if len(lines) != 1 {
		t.Fatalf("FormatWarnings() returned %d lines, want 1: %+v", len(lines), lines)
	}
	line := lines[0]
	for _, want := range []string{filterTestTypeA, filterTestProp, string(filter.Exact), filterTestValue} {
		if !strings.Contains(line, want) {
			t.Errorf("FormatWarnings() line %q does not mention %q", line, want)
		}
	}
}
