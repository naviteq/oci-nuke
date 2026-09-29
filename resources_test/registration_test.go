package ocinuke_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"

	// Blank-imported so registry.GetNames()/GetRegistrations() reflect the real, fully-populated
	// set of resource types this wave (Plans 04-03 through 04-10) registered -- the first place in
	// this codebase's tests that exercises the real resources/ package rather than a synthetic
	// registry.ClearRegistry()+Register fixture (04-RESEARCH.md Q3).
	_ "github.com/naviteq/oci-nuke/resources"
)

// minimumRegisteredTypes is the exact number of resource types registered across Phase 4, Phase 5,
// and Phase 6 combined. This lower bound is deliberately precise, not "at least 1": a future
// accidental deletion of an entire domain plan's file (or a registration that silently stops
// firing) must shrink registry.GetNames()'s count below this literal and fail this test, not pass
// silently. Update this literal only when a later phase deliberately adds to resources/ -- it must
// never shrink within Phase 6.
//
// 57 = 37 (Phase 4, Plans 04-04 through 04-10) + 19 (Phase 5's second coverage wave: Cluster,
// NodePool, ContainerRepository [05-01]; AutonomousDatabase, DbSystem, MySQLDbSystem [05-02];
// Application, Function [05-03]; Stream, StreamPool, NotificationTopic, Subscription, Rule
// [05-04]; Policy, DynamicGroup, TagNamespace, TagDefault [05-05]; Vault, KmsKey [05-06]) + 1
// (Phase 6: Compartment [06-02]).
const minimumRegisteredTypes = 57

// TestRegistrationGraph_NoCycleOrDanglingDependency proves registry.GetNames() -- which internally
// calls GetListersV2() -> graph.TopSort("root") -- does not panic against the real,
// fully-populated resources package, and that every name it returns resolves to an actual,
// non-nil registration. This is the only place in this codebase's history that exercises this path
// at all (04-RESEARCH.md Q3: "No such test exists in this codebase today"); without it, a
// DependsOn cycle or a misspelled dependency name (which topsort silently accepts as a
// nil-Resource phantom node, per GetListersV2()'s `r := registrations[name]; listers[name] =
// r.Lister` -- a nil-pointer panic waiting to happen) ships silently and only surfaces the first
// time an operator runs `oci-nuke resource-types`.
func TestRegistrationGraph_NoCycleOrDanglingDependency(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf(
				"registry.GetNames() panicked -- a DependsOn cycle or a misspelled dependency name "+
					"exists somewhere in resources/*.go (check every DependsOn entry against a real "+
					"registered resource type's Name constant): %v",
				r,
			)
		}
	}()

	names := registry.GetNames()
	if len(names) < minimumRegisteredTypes {
		t.Fatalf(
			"registry.GetNames() returned %d names, want at least %d -- this wave's registered "+
				"resource-type count must never shrink within Phase 5; check for an accidentally "+
				"deleted or no-longer-firing registration under resources/",
			len(names), minimumRegisteredTypes,
		)
	}

	for _, name := range names {
		reg := registry.GetRegistration(name)
		if reg == nil || reg.Resource == nil {
			t.Errorf(
				"registry.GetRegistration(%q).Resource is nil -- %q is a phantom graph node created "+
					"by some other registered type's DependsOn entry that misspells or otherwise fails "+
					"to name a real registered resource type; find the DependsOn entry that names %q "+
					"and fix the typo, or register %q for real",
				name, name, name, name,
			)
		}
	}
}

// TestEveryDependsOnEntryNamesARegisteredType is the check 04-11-PLAN.md's <critical> section adds
// beyond the four originally-planned checks: Plan 04-06 found that a DependsOn entry naming a type
// that is not registered makes the *depending* resource silently invisible to
// registry.GetNames() -- a single typo in a DependsOn string can switch a resource type off
// entirely, with no error and no failing test, unless something checks it directly. This test
// walks every registration's DependsOn list via registry.GetRegistrations() (which reflects every
// type that actually called ocinuke.Register, independent of whether the graph resolves cleanly)
// and fails, naming both the depending type and the unresolved dependency, if that dependency name
// is not itself a registered type. Unlike TestRegistrationGraph_NoCycleOrDanglingDependency (which
// can only report "GetNames() panicked" for a genuinely dangling entry), this test pinpoints the
// exact offending DependsOn entry even when the panic path would otherwise obscure it.
func TestEveryDependsOnEntryNamesARegisteredType(t *testing.T) {
	regs := registry.GetRegistrations()

	type violation struct {
		dependent  string
		dependency string
	}
	var violations []violation
	for name, reg := range regs {
		for _, dep := range reg.DependsOn {
			if _, ok := regs[dep]; !ok {
				violations = append(violations, violation{dependent: name, dependency: dep})
			}
		}
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].dependent != violations[j].dependent {
			return violations[i].dependent < violations[j].dependent
		}
		return violations[i].dependency < violations[j].dependency
	})

	for _, v := range violations {
		t.Errorf(
			"resource type %q declares DependsOn %q, which is not itself a registered resource "+
				"type -- this is a silent coverage hole: a typo here can switch %q's own "+
				"registration into a phantom graph node, and registry.GetNames() either panics or "+
				"silently drops it (04-RESEARCH.md Q3). Fix the typo in %q's DependsOn list, or "+
				"register the missing %q type",
			v.dependent, v.dependency, v.dependent, v.dependent, v.dependency,
		)
	}
}

// compartmentResourceTypeName MUST equal resources.CompartmentResourceType's value exactly
// ("Compartment") -- a bare string literal here, not an import of resources' own constant,
// mirroring pkg/commands/run/command.go's compartmentResourceTypeName and
// pkg/plan/compartment_leftover.go's compartmentEntryResourceType, both of which document the
// same cross-reference for the same reason.
const compartmentResourceTypeName = "Compartment"

// TestNoOtherRegisteredTypeDependsOnCompartment is the concrete, testable form of 06-CONTEXT.md's
// "sorts topologically last" instruction, per this plan's own <objective> note: Compartment's
// removal ordering relative to every other resource type comes exclusively from OCI's own
// async emptiness check (resources/compartment.go's HandleWait, re-attempting DeleteCompartment
// every round) plus Plan 06-03's deepest-first *libnuke.Nuke construction order
// (scope.Tree.DeletionOrder) -- never from the resource-type dependency graph DependsOn/topsort
// operates on. Per the pinned libnuke@v1.3.0 registry.go's own graph construction, a type with no
// DependsOn edges (Compartment included) gets only a "root" edge in the topological sort, so its
// position among every other no-dependency node is structurally UNCONSTRAINED -- asserting
// "Compartment sorts last" would pin down either a non-deterministic property or an accident of
// Go's file-lexical init() registration order, neither a real invariant this codebase can rely
// on. This test instead structurally forbids the actual regression that decision protects
// against, in its more useful inverted form: nothing may reintroduce a DependsOn edge pointing AT
// Compartment (the rotted-hand-maintained-list failure mode 06-CONTEXT.md forbids elsewhere,
// inverted -- a future contributor adding DependsOn: ["Compartment"] to some OTHER type would
// silently make that type wait on Compartment's own removal, which never legitimately happens in
// this design).
func TestNoOtherRegisteredTypeDependsOnCompartment(t *testing.T) {
	regs := registry.GetRegistrations()

	var violations []string
	for name, reg := range regs {
		if name == compartmentResourceTypeName {
			continue
		}
		for _, dep := range reg.DependsOn {
			if dep == compartmentResourceTypeName {
				violations = append(violations, name)
				break
			}
		}
	}
	sort.Strings(violations)

	for _, name := range violations {
		t.Errorf(
			"resource type %q declares DependsOn %q -- removal ordering relative to Compartment "+
				"must come exclusively from OCI's own async emptiness check plus deepest-first "+
				"compartment-tree construction order, never from the resource-type dependency "+
				"graph (06-CONTEXT.md). Remove %q from %q's DependsOn list",
			name, compartmentResourceTypeName, compartmentResourceTypeName, name,
		)
	}
}

// TestEveryRegisteredType_ImplementsUniqueKeyGetter enforces SAFE-09 by a test, not merely by
// convention: every registered type's Resource must implement resource.UniqueKeyGetter. Without
// UniqueKey(), libnuke falls back to matching re-listed resources on Properties(), which breaks
// the moment a property changes mid-run -- and in a Terraform-managed sandbox, resources get
// recreated with the same name.
func TestEveryRegisteredType_ImplementsUniqueKeyGetter(t *testing.T) {
	names := registry.GetNames()
	sort.Strings(names)

	var missing []string
	for _, name := range names {
		reg := registry.GetRegistration(name)
		if reg == nil {
			continue // covered by TestRegistrationGraph_NoCycleOrDanglingDependency
		}
		if _, ok := reg.Resource.(resource.UniqueKeyGetter); !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf(
			"the following registered resource type(s) do not implement resource.UniqueKeyGetter "+
				"(SAFE-09): %s -- without UniqueKey(), libnuke falls back to matching re-listed "+
				"resources on Properties(), which breaks when a property changes mid-run (e.g. a "+
				"Terraform-managed sandbox recreating a resource with the same name). Add a "+
				"`UniqueKey() string` method to each",
			strings.Join(missing, ", "),
		)
	}
}

// TestEveryRegisteredType_ImplementsFilter enforces, by a test rather than convention, that every
// registered type's Resource implements resource.Filter at all. A type with no Filter() cannot
// exclude a still-listed, already-removed resource and will hang the destructive run the first
// time HandleWait re-lists it (04-RESEARCH.md Q2). Note this only proves the interface is
// implemented -- resources_test/filter_contract_test.go separately proves no implementation is a
// lazy, unconditional `return nil` outside a reviewed allow-list.
func TestEveryRegisteredType_ImplementsFilter(t *testing.T) {
	names := registry.GetNames()
	sort.Strings(names)

	var missing []string
	for _, name := range names {
		reg := registry.GetRegistration(name)
		if reg == nil {
			continue // covered by TestRegistrationGraph_NoCycleOrDanglingDependency
		}
		if _, ok := reg.Resource.(resource.Filter); !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf(
			"the following registered resource type(s) do not implement resource.Filter: %s -- a "+
				"type with no Filter() cannot exclude a still-listed, already-removed resource and "+
				"will hang the destructive run the first time HandleWait re-lists it (04-RESEARCH.md "+
				"Q2). Add a `Filter() error` method to each",
			strings.Join(missing, ", "),
		)
	}
}
