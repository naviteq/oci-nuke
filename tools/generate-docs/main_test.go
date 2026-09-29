package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
)

// testAvailabilityDomainValue is the shared "availability_domain" string value used across this
// file's named-constant-resolution fixtures/assertions -- declared once so the raw string literal
// itself does not repeat 3+ times (goconst).
const testAvailabilityDomainValue = "availability_domain"

// testGadgetTypeName is the shared "Gadget" fixture type name reused across this file's run()
// tests (goconst min-occurrences 3), same rationale as testAvailabilityDomainValue above.
const testGadgetTypeName = "Gadget"

// parseFixture parses src (a standalone Go source string, never a real resources/*.go file --
// keeping this test independent of whatever Wave 3's domain plans have landed) as a single file.
func parseFixture(t *testing.T, src string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, parser.AllErrors)
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return f
}

// parseFixtureWithComments is parseFixture's own sibling for extractNotes' tests specifically --
// parser.ParseComments is required for a declaration's Doc comment (GenDecl.Doc/TypeSpec.Doc) to
// be populated at all; parseFixture's plain parser.AllErrors mode leaves both nil, which would
// make every extractNotes test below vacuously pass (a nil Doc and a "no Notes: paragraph" Doc
// both return "").
func parseFixtureWithComments(t *testing.T, src string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, parser.ParseComments|parser.AllErrors)
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return f
}

// TestExtractPropertyKeys_LiteralSetCalls is the plan's core behavior: a Properties() method
// containing .Set("id", ...).Set("name", ...) calls extracts exactly ["id", "name"].
func TestExtractPropertyKeys_LiteralSetCalls(t *testing.T) {
	const src = `
package resources

func (r *Widget) Properties() types.Properties {
	return types.NewProperties().
		Set("id", r.id).
		Set("name", r.name)
}
`
	got := extractPropertyKeys(parseFixture(t, src), nil)
	want := []string{keyID, keyName}
	assertStringSlicesEqual(t, got, want)
}

// TestExtractPropertyKeys_SkipsSetTagWithPrefixAndDynamicKeys proves SetTagWithPrefix is
// deliberately skipped (its key argument is a per-instance tag name, not a static property name)
// and that a non-literal Set/SetTag key argument (a variable, as resources/support.go's own
// SetTag(&k, v) tag-flattening loop uses) contributes no key.
func TestExtractPropertyKeys_SkipsSetTagWithPrefixAndDynamicKeys(t *testing.T) {
	const src = `
package resources

func (r *Widget) Properties() types.Properties {
	props := types.NewProperties().Set("id", r.id)
	for k, v := range r.tags {
		props.SetTag(&k, v)
	}
	props.SetTagWithPrefix("ns", &someKey, someVal)
	return props
}
`
	got := extractPropertyKeys(parseFixture(t, src), nil)
	want := []string{keyID}
	assertStringSlicesEqual(t, got, want)
}

// TestExtractPropertyKeys_ResolvesNamedStringConstants proves a Set(propFoo, ...) call --
// resources/support.go's own goconst-driven convention for every property key reused 3+ times
// across resource files -- documents exactly the constant's string value, the same as if the
// literal had been written inline. This is the exact gap Plan 04-11 found the first time
// tools/generate-docs was run against the real, fully-populated resources package: Instance,
// Bucket, MountTarget, and most other wave-4 types set several properties via named propX
// constants (not inline literals), and without this resolution those keys were silently missing
// from every generated doc.
func TestExtractPropertyKeys_ResolvesNamedStringConstants(t *testing.T) {
	const src = `
package resources

func (r *Widget) Properties() types.Properties {
	return types.NewProperties().
		Set("id", r.id).
		Set(propAvailabilityDomain, r.ad)
}
`
	constants := map[string]string{"propAvailabilityDomain": testAvailabilityDomainValue}
	got := extractPropertyKeys(parseFixture(t, src), constants)
	want := []string{testAvailabilityDomainValue, keyID}
	assertStringSlicesEqual(t, got, want)
}

// TestExtractPropertyKeys_UnresolvedIdentifierContributesNoKey proves an identifier argument that
// is not present in the constants map is skipped rather than panicking or documenting a bogus
// key -- e.g. a local variable holding a dynamically computed key, or a constant declared
// somewhere other than support.go this run didn't load.
func TestExtractPropertyKeys_UnresolvedIdentifierContributesNoKey(t *testing.T) {
	const src = `
package resources

func (r *Widget) Properties() types.Properties {
	return types.NewProperties().
		Set("id", r.id).
		Set(someUnknownConst, r.x)
}
`
	got := extractPropertyKeys(parseFixture(t, src), map[string]string{})
	want := []string{keyID}
	assertStringSlicesEqual(t, got, want)
}

// TestExtractPropertyKeys_BaseProperties proves a Properties() method that delegates to the
// shared baseProperties helper (resources/support.go -- the shape cmd/gen-resource's scaffold
// template emits, 04-03-PLAN.md Task 1) is still documented with its fixed baseline key set,
// rather than producing an empty (and misleadingly undocumented) property list.
func TestExtractPropertyKeys_BaseProperties(t *testing.T) {
	const src = `
package resources

func (r *Widget) Properties() types.Properties {
	x := r.widget
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
`
	got := extractPropertyKeys(parseFixture(t, src), nil)
	want := []string{keyCompartmentID, keyID, keyLifecycleState, keyName, keyTimeCreated}
	assertStringSlicesEqual(t, got, want)
}

// TestExtractPropertyKeys_BasePropertiesPlusExtras proves the baseProperties baseline and extra,
// hand-added literal Set(...) calls (e.g. a type-specific field a domain-plan author added by
// hand after scaffolding) both contribute to the same documented set.
func TestExtractPropertyKeys_BasePropertiesPlusExtras(t *testing.T) {
	const src = `
package resources

func (r *Widget) Properties() types.Properties {
	x := r.widget
	props := baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
	props.Set("vcn_id", x.VcnId)
	return props
}
`
	got := extractPropertyKeys(parseFixture(t, src), nil)
	want := []string{keyCompartmentID, keyID, keyLifecycleState, keyName, keyTimeCreated, "vcn_id"}
	assertStringSlicesEqual(t, got, want)
}

// widgetNotesFixtureSrc is the standalone-declaration shape every real resources/*.go file
// actually uses (`type Widget struct { ... }`, doc comment attaching to the GenDecl, not a
// per-spec TypeSpec.Doc) -- the shape extractNotes' fallback-to-gen.Doc branch exists for.
const widgetNotesFixtureSrc = `
package resources

// Widget wraps one thing.
//
// Notes: Widgets are enumerated unconditionally, with no relationship to any other thing -- this
// is proven by TestWidgetIndependentEnumeration.
type Widget struct {
	id string
}
`

// TestExtractNotes_StandaloneTypeDecl_FindsNotesInGenDeclDoc proves extractNotes finds and
// extracts a Notes: paragraph from a standalone `type X struct { ... }` declaration's own doc
// comment -- the shape every real resources/*.go file uses -- trimmed, with internal line breaks
// preserved exactly as written.
func TestExtractNotes_StandaloneTypeDecl_FindsNotesInGenDeclDoc(t *testing.T) {
	got := extractNotes(parseFixtureWithComments(t, widgetNotesFixtureSrc), "Widget")
	want := "Widgets are enumerated unconditionally, with no relationship to any other thing -- this\n" +
		"is proven by TestWidgetIndependentEnumeration."
	if got != want {
		t.Errorf("extractNotes() = %q, want %q", got, want)
	}
}

// TestExtractNotes_NoNotesParagraph_ReturnsEmpty proves a type whose doc comment has no Notes:
// paragraph at all (the common case -- most types document nothing beyond Scope/DependsOn/
// Properties) returns "", not an error and not the whole doc comment.
func TestExtractNotes_NoNotesParagraph_ReturnsEmpty(t *testing.T) {
	const src = `
package resources

// Sprocket wraps one other thing, with nothing further to document.
type Sprocket struct {
	id string
}
`
	got := extractNotes(parseFixtureWithComments(t, src), "Sprocket")
	if got != "" {
		t.Errorf("extractNotes() = %q, want empty", got)
	}
}

// TestExtractNotes_NoDocCommentAtAll_ReturnsEmpty proves a type declaration with no doc comment
// at all -- not even a one-line description -- returns "" rather than panicking on a nil Doc.
func TestExtractNotes_NoDocCommentAtAll_ReturnsEmpty(t *testing.T) {
	const src = `
package resources

type Gizmo struct {
	id string
}
`
	got := extractNotes(parseFixtureWithComments(t, src), "Gizmo")
	if got != "" {
		t.Errorf("extractNotes() = %q, want empty", got)
	}
}

// TestExtractNotes_TypeNotFound_ReturnsEmpty proves asking for a type name the file does not
// declare at all returns "" rather than panicking.
func TestExtractNotes_TypeNotFound_ReturnsEmpty(t *testing.T) {
	got := extractNotes(parseFixtureWithComments(t, widgetNotesFixtureSrc), "DoesNotExist")
	if got != "" {
		t.Errorf("extractNotes() = %q, want empty", got)
	}
}

// TestNotesForType_ReadsRealFileFromDisk proves notesForType (the propertyKeysForType-mirroring,
// disk-reading half extractNotes' own unit tests above do not exercise) locates srcDir/widget.go
// via the same sourceFilePath convention propertyKeysForType uses, and returns the same Notes
// content extractNotes would against the parsed file directly.
func TestNotesForType_ReadsRealFileFromDisk(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "widget.go"), []byte(widgetNotesFixtureSrc), 0o600); err != nil {
		t.Fatalf("writing widget fixture source: %v", err)
	}

	got, err := notesForType(srcDir, "Widget")
	if err != nil {
		t.Fatalf("notesForType() error = %v", err)
	}
	want := "Widgets are enumerated unconditionally, with no relationship to any other thing -- this\n" +
		"is proven by TestWidgetIndependentEnumeration."
	if got != want {
		t.Errorf("notesForType() = %q, want %q", got, want)
	}
}

// TestExtractPropertyKeys_NoPropertiesMethod proves a file with no Properties() method at all
// returns an empty (nil) result rather than panicking.
func TestExtractPropertyKeys_NoPropertiesMethod(t *testing.T) {
	const src = `
package resources

func (r *Widget) Remove() error { return nil }
`
	got := extractPropertyKeys(parseFixture(t, src), nil)
	if len(got) != 0 {
		t.Fatalf("extractPropertyKeys() = %v, want empty", got)
	}
}

// TestLoadPropertyConstants_ParsesStringConstBlock proves loadPropertyConstants extracts every
// `identifier = "value"` entry from a support.go-shaped const block, mirroring
// resources/support.go's own property-key constant convention.
func TestLoadPropertyConstants_ParsesStringConstBlock(t *testing.T) {
	const src = `
package resources

const (
	propID                 = "id"
	propAvailabilityDomain = "availability_domain"
)
`
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, supportFileName), []byte(src), 0o600); err != nil {
		t.Fatalf("writing support.go fixture: %v", err)
	}

	got, err := loadPropertyConstants(srcDir)
	if err != nil {
		t.Fatalf("loadPropertyConstants() error = %v", err)
	}

	want := map[string]string{"propID": "id", "propAvailabilityDomain": testAvailabilityDomainValue}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadPropertyConstants() = %v, want %v", got, want)
	}
}

// TestLoadPropertyConstants_NoSupportFile proves a srcDir with no support.go (this file's own
// fixture-based TestRun_WritesDeterministicDoc, for example) returns an empty, non-nil map and no
// error -- named-constant resolution is additive, never required.
func TestLoadPropertyConstants_NoSupportFile(t *testing.T) {
	got, err := loadPropertyConstants(t.TempDir())
	if err != nil {
		t.Fatalf("loadPropertyConstants() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("loadPropertyConstants() = %v, want empty", got)
	}
}

// testScope is the registry.Scope value every synthetic registration in this file uses.
const testScope = "compartment"

// fakeLister is a minimal registry.Lister -- run() never calls List(), so this only needs to
// satisfy the interface.
type fakeLister struct{}

func (fakeLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	return nil, nil
}

const widgetFixtureSrc = `
package resources

func (r *Widget) Properties() types.Properties {
	x := r.widget
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
`

const gadgetFixtureSrc = `
package resources

func (r *Gadget) Properties() types.Properties {
	return types.NewProperties().Set("id", r.id)
}
`

// TestRun_WritesDeterministicDoc proves run() writes docs/resources/<Type>.md containing the
// type name, Scope, DependsOn, and property keys, and that running it twice against the same
// registration/source state produces byte-identical output -- safe to re-run in CI without
// spurious diffs.
//
// Gadget and Doohickey are registered too (not just referenced via Widget's DependsOn):
// registry.GetNames() is built from GetListersV2(), which topologically sorts from a "root" node
// that only has edges to types with an EMPTY DependsOn (registry.Register's own graph-building
// logic) -- a DependsOn naming a type that was never itself registered leaves the dependent type
// unreachable from "root" and silently absent from GetNames(). Mirroring that real graph shape
// here is what makes this test representative of how the real resources/ package's dependency
// graph actually resolves once Wave 3's domain plans populate it.
func TestRun_WritesDeterministicDoc(t *testing.T) {
	registry.ClearRegistry()
	t.Cleanup(registry.ClearRegistry)

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "widget.go"), []byte(widgetFixtureSrc), 0o600); err != nil {
		t.Fatalf("writing widget fixture source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "gadget.go"), []byte(gadgetFixtureSrc), 0o600); err != nil {
		t.Fatalf("writing gadget fixture source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "doohickey.go"), []byte(gadgetFixtureSrc), 0o600); err != nil {
		t.Fatalf("writing doohickey fixture source: %v", err)
	}

	registry.Register(&registry.Registration{
		Name:     testGadgetTypeName,
		Scope:    testScope,
		Resource: &struct{}{},
		Lister:   fakeLister{},
	})
	registry.Register(&registry.Registration{
		Name:     "Doohickey",
		Scope:    testScope,
		Resource: &struct{}{},
		Lister:   fakeLister{},
	})
	registry.Register(&registry.Registration{
		Name:      "Widget",
		Scope:     testScope,
		Resource:  &struct{}{},
		Lister:    fakeLister{},
		DependsOn: []string{testGadgetTypeName, "Doohickey"},
	})

	outDir1 := t.TempDir()
	if err := run(srcDir, outDir1); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	outDir2 := t.TempDir()
	if err := run(srcDir, outDir2); err != nil {
		t.Fatalf("run() (second pass) error = %v", err)
	}

	got1, err := os.ReadFile(filepath.Join(outDir1, "Widget.md"))
	if err != nil {
		t.Fatalf("reading first-pass doc: %v", err)
	}
	got2, err := os.ReadFile(filepath.Join(outDir2, "Widget.md"))
	if err != nil {
		t.Fatalf("reading second-pass doc: %v", err)
	}
	if !bytes.Equal(got1, got2) {
		t.Fatalf("run() is not deterministic:\n--- pass 1 ---\n%s\n--- pass 2 ---\n%s", got1, got2)
	}

	content := string(got1)
	for _, want := range []string{"# Widget", "Scope: compartment", "DependsOn: Doohickey, Gadget", "`id`", "`compartment_id`"} {
		if !bytes.Contains(got1, []byte(want)) {
			t.Errorf("doc content = %q, want to contain %q", content, want)
		}
	}
}

// gizmoFixtureSrc uses a named propX constant (declared in supportFixtureSrc below), the way
// nearly every real wave-4 resources/*.go file documents its own type-specific properties, rather
// than a `.Set("literal", ...)` call.
const gizmoFixtureSrc = `
package resources

func (r *Gizmo) Properties() types.Properties {
	return types.NewProperties().
		Set("id", r.id).
		Set(propAvailabilityDomain, r.ad)
}
`

const supportFixtureSrc = `
package resources

const propAvailabilityDomain = "availability_domain"
`

// TestRun_ResolvesNamedPropertyConstantsFromSupportFile proves run() end to end resolves a
// Set(propFoo, ...) call the same way it resolves a literal, by loading srcDir/support.go once
// and threading the resulting constants map through propertyKeysForType. This is the exact defect
// Plan 04-11 found the first time tools/generate-docs ran against the real, fully-populated
// resources package -- most types document some of their properties via support.go's shared propX
// constants, and without this resolution those keys were silently absent from every generated
// doc.
func TestRun_ResolvesNamedPropertyConstantsFromSupportFile(t *testing.T) {
	registry.ClearRegistry()
	t.Cleanup(registry.ClearRegistry)

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "gizmo.go"), []byte(gizmoFixtureSrc), 0o600); err != nil {
		t.Fatalf("writing gizmo fixture source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, supportFileName), []byte(supportFixtureSrc), 0o600); err != nil {
		t.Fatalf("writing support.go fixture: %v", err)
	}

	registry.Register(&registry.Registration{
		Name:     "Gizmo",
		Scope:    testScope,
		Resource: &struct{}{},
		Lister:   fakeLister{},
	})

	outDir := t.TempDir()
	if err := run(srcDir, outDir); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(outDir, "Gizmo.md"))
	if err != nil {
		t.Fatalf("reading Gizmo.md: %v", err)
	}
	for _, want := range []string{"`id`", "`availability_domain`"} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("Gizmo.md content = %q, want to contain %q", string(got), want)
		}
	}
}

// widgetWithNotesFixtureSrc is widgetNotesFixtureSrc's own Properties()-bearing sibling -- run()
// needs a real Properties() method present to exercise the full pipeline the way a real
// resources/*.go file does, not just extractNotes' own narrower unit tests above.
const widgetWithNotesFixtureSrc = `
package resources

// Widget wraps one thing.
//
// Notes: Widgets are enumerated unconditionally, with no relationship to any other thing -- this
// is proven by TestWidgetIndependentEnumeration.
type Widget struct {
	id string
}

func (r *Widget) Properties() types.Properties {
	return types.NewProperties().Set("id", r.id)
}
`

// TestRun_RendersNotesSectionWhenTypeDocCommentHasOne is this plan's own regression guard against
// the exact defect Plan 06-02 hit and reverted (deferred-items.md): run() must render a type's
// Notes: doc-comment paragraph as a trailing "## Notes" section, and MUST NOT render one at all
// for a type whose doc comment carries no such paragraph (Gadget/Doohickey, registered alongside
// Widget in this same run(), have no Properties()/no doc comment and must never gain a spurious
// "## Notes" heading of their own).
func TestRun_RendersNotesSectionWhenTypeDocCommentHasOne(t *testing.T) {
	registry.ClearRegistry()
	t.Cleanup(registry.ClearRegistry)

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "widget.go"), []byte(widgetWithNotesFixtureSrc), 0o600); err != nil {
		t.Fatalf("writing widget fixture source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "gadget.go"), []byte(gadgetFixtureSrc), 0o600); err != nil {
		t.Fatalf("writing gadget fixture source: %v", err)
	}

	registry.Register(&registry.Registration{
		Name:     "Widget",
		Scope:    testScope,
		Resource: &struct{}{},
		Lister:   fakeLister{},
	})
	registry.Register(&registry.Registration{
		Name:     testGadgetTypeName,
		Scope:    testScope,
		Resource: &struct{}{},
		Lister:   fakeLister{},
	})

	outDir := t.TempDir()
	if err := run(srcDir, outDir); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	widgetDoc, err := os.ReadFile(filepath.Join(outDir, "Widget.md"))
	if err != nil {
		t.Fatalf("reading Widget.md: %v", err)
	}
	wantWidget := "## Notes\n\nWidgets are enumerated unconditionally, with no relationship to any other thing -- this\n" +
		"is proven by TestWidgetIndependentEnumeration.\n"
	if !bytes.Contains(widgetDoc, []byte(wantWidget)) {
		t.Errorf("Widget.md = %q, want it to contain %q", string(widgetDoc), wantWidget)
	}

	gadgetDoc, err := os.ReadFile(filepath.Join(outDir, "Gadget.md"))
	if err != nil {
		t.Fatalf("reading Gadget.md: %v", err)
	}
	if bytes.Contains(gadgetDoc, []byte("## Notes")) {
		t.Errorf("Gadget.md = %q, want no \"## Notes\" section (Gadget's doc comment has no Notes: paragraph)", string(gadgetDoc))
	}
}

func assertStringSlicesEqual(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
