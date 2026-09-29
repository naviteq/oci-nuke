package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ekristen/libnuke/pkg/filter"
)

const (
	testPresetName   = "p1"
	testNameProperty = "Name"
	testPresetValue  = "terraform-state"
)

// TestResolveFilters_MergesPresetIntoCompartmentOwnFilters proves CONF-04: a compartment's own
// filters and every preset it references are merged into one filter.Filters, keyed by
// resource-type, via filter.Filters.Append -- not a hand-rolled merge.
func TestResolveFilters_MergesPresetIntoCompartmentOwnFilters(t *testing.T) {
	c := &Config{
		Presets: map[string]Preset{
			testPresetName: {Filters: filter.Filters{
				"Bucket": {{Type: filter.Contains, Property: testNameProperty, Value: testPresetValue}},
			}},
		},
		Filters: map[string]CompartmentFilters{
			"ocidA": {
				Filters: filter.Filters{
					"Instance": {{Type: filter.Glob, Property: testNameProperty, Value: "keep-*"}},
				},
				Presets: []string{testPresetName},
			},
		},
	}

	got, err := c.ResolveFilters("ocidA")
	if err != nil {
		t.Fatalf("ResolveFilters(ocidA) returned error: %v", err)
	}

	if len(got["Instance"]) != 1 || got["Instance"][0].Value != "keep-*" {
		t.Errorf("got[Instance] = %+v, want the compartment's own Instance filter preserved", got["Instance"])
	}
	if len(got["Bucket"]) != 1 || got["Bucket"][0].Value != testPresetValue {
		t.Errorf("got[Bucket] = %+v, want p1's Bucket filter merged in", got["Bucket"])
	}
}

// TestResolveFilters_SamePresetReusedAcrossTwoCompartments proves CONF-04's literal wording:
// the SAME named preset, referenced from two different compartments with no own filters of
// their own, resolves to identical filter sets for both.
func TestResolveFilters_SamePresetReusedAcrossTwoCompartments(t *testing.T) {
	c := &Config{
		Presets: map[string]Preset{
			testPresetName: {Filters: filter.Filters{
				"Bucket": {{Type: filter.Contains, Property: testNameProperty, Value: testPresetValue}},
			}},
		},
		Filters: map[string]CompartmentFilters{
			"ocidA": {Presets: []string{testPresetName}},
			"ocidB": {Presets: []string{testPresetName}},
		},
	}

	gotA, err := c.ResolveFilters("ocidA")
	if err != nil {
		t.Fatalf("ResolveFilters(ocidA) returned error: %v", err)
	}
	gotB, err := c.ResolveFilters("ocidB")
	if err != nil {
		t.Fatalf("ResolveFilters(ocidB) returned error: %v", err)
	}

	if !reflect.DeepEqual(gotA, gotB) {
		t.Errorf("ResolveFilters(ocidA) = %+v, ResolveFilters(ocidB) = %+v, want equal -- same preset reused across two compartments", gotA, gotB)
	}
	if len(gotA["Bucket"]) != 1 || gotA["Bucket"][0].Value != testPresetValue {
		t.Errorf("gotA[Bucket] = %+v, want p1's Bucket filter", gotA["Bucket"])
	}
}

// TestResolveFilters_NoEntryReturnsEmptyNotError proves that a compartment absent from
// Config.Filters entirely is not an error -- it simply has zero filters configured.
func TestResolveFilters_NoEntryReturnsEmptyNotError(t *testing.T) {
	c := &Config{}

	got, err := c.ResolveFilters("ocidC")
	if err != nil {
		t.Fatalf("ResolveFilters(ocidC) returned error: %v, want nil", err)
	}
	if got == nil {
		t.Fatal("ResolveFilters(ocidC) returned a nil map, want an empty, non-nil filter.Filters")
	}
	if len(got) != 0 {
		t.Errorf("ResolveFilters(ocidC) = %+v, want empty", got)
	}
}

// TestResolveFilters_UnknownPresetErrors proves a referenced-but-undefined preset name is a
// loud error (almost certainly an operator typo), not a silent no-op.
func TestResolveFilters_UnknownPresetErrors(t *testing.T) {
	c := &Config{
		Filters: map[string]CompartmentFilters{
			"ocidD": {Presets: []string{"does-not-exist"}},
		},
	}

	_, err := c.ResolveFilters("ocidD")
	if err == nil {
		t.Fatal("ResolveFilters(ocidD) = nil error, want an error naming the unknown preset")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error = %q, want it to contain the unknown preset name %q", err.Error(), "does-not-exist")
	}
}
