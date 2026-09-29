package ocinuke_test

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/filter"
	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/queue"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/scanner"
	"github.com/ekristen/libnuke/pkg/types"
)

// vocabResource is a fake resource used only to exercise the REAL libnuke filter engine (via
// libnuke.New + n.Run), not a reimplementation of it -- mirroring dryrun_test.go's own stated
// purpose for this test file. It implements resource.Resource + resource.PropertyGetter, and
// exposes a Name property (for exact/glob/regex/prefix/contains/invert cases) and a
// time-created property (for the dateOlderThan cases), following the exact idiom
// 02-RESEARCH.md Q4 documents: types.Properties.Set has a `case time.Time:` branch that formats
// RFC3339, which dateOlderThan's parseDate round-trips cleanly.
type vocabResource struct {
	name      string
	createdAt time.Time
}

func (r *vocabResource) Remove(_ context.Context) error { return nil }
func (r *vocabResource) UniqueKey() string              { return r.name }
func (r *vocabResource) Properties() types.Properties {
	props := types.NewProperties()
	props.Set("Name", r.name)
	props.Set("time-created", r.createdAt)
	return props
}

// vocabLister returns exactly one fixed resource. Each vocabulary case below gets its own
// resource TYPE (registered under its own name) with its own single-entry filter list, so each
// case is fully isolated from every other -- filter.Filters is keyed by resource type, and
// filterWithoutGroups (libnuke's OR-across-filters default, see 02-RESEARCH.md Q4) only ever
// evaluates the filters registered under a given item's own Type. Isolating one filter type per
// resource type, rather than stacking every vocabulary type onto one shared resource type, is
// what lets this test assert "a resource matching only the regex filter is excluded and no
// other vocabulary type substitutes for it" without cross-contamination from unrelated filters.
type vocabLister struct{ resource resource.Resource }

func (l *vocabLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	return []resource.Resource{l.resource}, nil
}

// vocabCase names one resource type, its single resource, and the one filter registered under
// that type's key -- plus whether the real engine is expected to filter (exclude/protect) it.
type vocabCase struct {
	typeName     string
	res          *vocabResource
	f            filter.Filter
	wantFiltered bool
}

const propertyName = "Name"

// runVocabCases registers each case as its own resource type against the real libnuke.Nuke,
// runs a dry-run scan (Filter() runs during Scan itself -- see nuke.go's runScanner, which calls
// n.Filter(item) per item as it is scanned, before the dry-run-vs-no-dry-run branch in Run() is
// even reached), and asserts each item's resulting queue state matches wantFiltered.
func runVocabCases(t *testing.T, cases []vocabCase) {
	t.Helper()
	registry.ClearRegistry()
	t.Cleanup(restoreRealRegistry)

	filters := filter.Filters{}
	resourceTypes := make([]string, 0, len(cases))
	for i := range cases {
		c := &cases[i]
		filters[c.typeName] = []filter.Filter{c.f}
		resourceTypes = append(resourceTypes, c.typeName)
		registry.Register(&registry.Registration{
			Name:   c.typeName,
			Scope:  "test-scope",
			Lister: &vocabLister{resource: c.res},
		})
	}

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:   false, // dry-run only, per this phase's live-tenancy read-only policy
		Force:      true,
		ForceSleep: 3,
	}, filters, nil)

	s, err := scanner.New(&scanner.Config{
		Owner:         "test-owner",
		ResourceTypes: resourceTypes,
	})
	if err != nil {
		t.Fatalf("scanner.New: %v", err)
	}
	if err := n.RegisterScanner("test-scope", s); err != nil {
		t.Fatalf("RegisterScanner: %v", err)
	}

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	gotByType := make(map[string]*queue.Item, len(cases))
	for _, item := range n.Queue.GetItems() {
		gotByType[item.Type] = item
	}

	for i := range cases {
		c := &cases[i]
		item, ok := gotByType[c.typeName]
		if !ok {
			t.Errorf("no queue item found for resource type %q", c.typeName)
			continue
		}
		gotFiltered := item.State == queue.ItemStateFiltered
		if gotFiltered != c.wantFiltered {
			t.Errorf("type %q (resource %q): item.State = %s, wantFiltered = %v",
				c.typeName, c.res.name, item.State.String(), c.wantFiltered)
		}
	}
}

// TestFilterEngine_FullVocabularyAndInversion proves CONF-05's full filter vocabulary --
// exact, glob, regex, prefix, contains, and inversion -- against the REAL, unmocked
// libnuke.Nuke.Filter path, not a reimplementation. regex is asserted as its own distinct case,
// matched with an actual regular expression (not a glob pattern relabeled), per 02-CONTEXT.md
// locked requirement 9 naming it explicitly.
func TestFilterEngine_FullVocabularyAndInversion(t *testing.T) {
	now := time.Now()

	cases := []vocabCase{
		{
			typeName:     "ExactRes",
			res:          &vocabResource{name: "exact-match-target", createdAt: now},
			f:            filter.Filter{Type: filter.Exact, Property: propertyName, Value: "exact-match-target"},
			wantFiltered: true,
		},
		{
			typeName:     "GlobRes",
			res:          &vocabResource{name: "glob-xyz-target", createdAt: now},
			f:            filter.Filter{Type: filter.Glob, Property: propertyName, Value: "glob-*-target"},
			wantFiltered: true,
		},
		{
			typeName:     "RegexRes",
			res:          &vocabResource{name: "regex-42-target", createdAt: now},
			f:            filter.Filter{Type: filter.Regex, Property: propertyName, Value: `^regex-[0-9]+-target$`},
			wantFiltered: true,
		},
		{
			// Proves regex is genuinely regex matching, not a glob pattern substituted under a
			// different type name: this value would NOT match as a glob (no glob metacharacter
			// semantics for character classes), but must still match via real regexp.Compile.
			typeName:     "RegexRes_NoMatch",
			res:          &vocabResource{name: "not-a-regex-match", createdAt: now},
			f:            filter.Filter{Type: filter.Regex, Property: propertyName, Value: `^regex-[0-9]+-target$`},
			wantFiltered: false,
		},
		{
			typeName:     "PrefixRes",
			res:          &vocabResource{name: "prefix-item", createdAt: now},
			f:            filter.Filter{Type: filter.Prefix, Property: propertyName, Value: "prefix-"},
			wantFiltered: true,
		},
		{
			typeName:     "ContainsRes",
			res:          &vocabResource{name: "has-contains-marker-in-name", createdAt: now},
			f:            filter.Filter{Type: filter.Contains, Property: propertyName, Value: "contains-marker"},
			wantFiltered: true,
		},
		{
			// Invert case (CONF-05 explicitly requires inversion coverage): the underlying,
			// non-inverted filter is `Name == "not-invert-target"`, which is false for this
			// resource ("invert-target" != "not-invert-target"). With Invert: true, the match
			// result is flipped to true, so the resource IS filtered/excluded -- proving
			// invert actually flips the engine's own match result, not a hand-rolled negation.
			typeName:     "InvertRes",
			res:          &vocabResource{name: "invert-target", createdAt: now},
			f:            filter.Filter{Type: filter.Exact, Property: propertyName, Value: "not-invert-target", Invert: true},
			wantFiltered: true,
		},
	}

	runVocabCases(t, cases)
}

// TestFilterEngine_DateOlderThanPolarity pins the single most dangerous fact in this plan a
// second time, through the real engine (Task 2's Evaluate pins it once already, independently):
// dateOlderThan protects anything YOUNGER than Value and exposes anything older -- the inverse
// of what the name suggests. Uses the exact worked-example numbers from 02-RESEARCH.md Q4 and
// 02-03-PLAN.md's objective: a 1h-old resource against "24h" is filtered (protected); a
// 30-day-old resource against the same "24h" is NOT filtered (eligible).
func TestFilterEngine_DateOlderThanPolarity(t *testing.T) {
	now := time.Now()

	cases := []vocabCase{
		{
			typeName:     "DateOlderThanYoungRes",
			res:          &vocabResource{name: "young-resource", createdAt: now.Add(-1 * time.Hour)},
			f:            filter.Filter{Type: filter.DateOlderThan, Property: "time-created", Value: "24h"},
			wantFiltered: true, // 1h-ago + 24h = 23h-in-future, After(now) = true -> protected
		},
		{
			typeName:     "DateOlderThanOldRes",
			res:          &vocabResource{name: "old-resource", createdAt: now.Add(-30 * 24 * time.Hour)},
			f:            filter.Filter{Type: filter.DateOlderThan, Property: "time-created", Value: "24h"},
			wantFiltered: false, // 30d-ago + 24h = 29d-ago, After(now) = false -> not protected, eligible
		},
	}

	runVocabCases(t, cases)
}
