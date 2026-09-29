// Command generate-docs walks every resource type registered with libnuke's registry and emits
// one docs/resources/<Type>.md per type, listing its Scope, DependsOn, and the exact property
// keys its Properties() method sets -- parsed back out of the type's own source file via go/ast,
// never hand-duplicated (DOC-04). Deterministic by construction (sorted type list, sorted
// property keys, sorted DependsOn) so it is safe to re-run in CI without spurious diffs.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/ekristen/libnuke/pkg/registry"

	// Blank-imported so registry.GetNames() reflects every real registered type when this
	// command actually runs against the populated resources/ package.
	_ "github.com/naviteq/oci-nuke/resources"
)

const (
	defaultSrcDir = "resources"
	defaultOutDir = "docs/resources"
)

// Property key constants mirroring resources/support.go's own propX constants -- declared here
// too (not imported; they are unexported in that package) so this tool and its tests never
// repeat the raw string literal (goconst, min-occurrences: 3).
const (
	keyID             = "id"
	keyCompartmentID  = "compartment_id"
	keyName           = "name"
	keyLifecycleState = "lifecycle_state"
	keyTimeCreated    = "time_created"
)

// baseDocumentedKeys mirrors resources/support.go's baseProperties -- the fixed set of property
// keys that helper can set (each conditionally, depending on whether the underlying SDK field is
// present on a given instance, but always part of a type's documented filter vocabulary the
// moment its Properties() method delegates to baseProperties, per 04-RESEARCH.md Q6).
var baseDocumentedKeys = []string{keyID, keyCompartmentID, keyName, keyLifecycleState, keyTimeCreated}

func main() {
	if err := run(defaultSrcDir, defaultOutDir); err != nil {
		fmt.Fprintln(os.Stderr, "generate-docs:", err)
		os.Exit(1)
	}
}

// supportFileName is the file, by convention (resources/support.go), that declares the shared
// propX identifier -> string-literal constants a domain resource file's Properties() method may
// reference instead of repeating a raw string literal (support.go's own goconst-driven
// convention, min-occurrences 3, enforced by Plan 04-12's lint gate). run() loads it once per
// invocation so extractPropertyKeys can resolve `Set(propFoo, ...)` the same way it already
// resolves `Set("foo", ...)`.
const supportFileName = "support.go"

// run writes one <outDir>/<Type>.md per name returned by registry.GetNames(), reading each
// type's source file from srcDir by the same snake_case(Name)+".go" convention
// cmd/gen-resource's scaffold generator writes to.
func run(srcDir, outDir string) error {
	names := registry.GetNames()
	sort.Strings(names)

	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", outDir, err)
	}

	constants, err := loadPropertyConstants(srcDir)
	if err != nil {
		return fmt.Errorf("loading property key constants: %w", err)
	}

	index := make([]indexEntry, 0, len(names))

	for _, name := range names {
		reg := registry.GetRegistration(name)
		if reg == nil {
			continue // unreachable in practice: GetNames() only returns names GetRegistration resolves
		}

		keys, err := propertyKeysForType(srcDir, name, constants)
		if err != nil {
			return fmt.Errorf("extracting property keys for %s: %w", name, err)
		}

		notes, err := notesForType(srcDir, name)
		if err != nil {
			return fmt.Errorf("extracting notes for %s: %w", name, err)
		}

		path := filepath.Join(outDir, name+".md")
		if err := os.WriteFile(path, []byte(renderDoc(name, reg, keys, notes)), 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		index = append(index, indexEntry{Name: name, Scope: string(reg.Scope), AltResource: reg.AlternativeResource})
	}

	indexPath := filepath.Join(outDir, indexFileName)
	if err := os.WriteFile(indexPath, []byte(renderIndex(index)), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", indexPath, err)
	}
	return nil
}

// indexFileName is the resource-type index. Generated from the registry for the same reason the
// per-type pages are: a hand-listed index is wrong by the next coverage wave (DOC-04, NR-814).
const indexFileName = "index.md"

type indexEntry struct {
	Name        string
	Scope       string
	AltResource string
}

// renderIndex builds the one page a visitor uses to answer "is my resource type covered?".
// Grouped by scope, because that is the axis that decides whether a type is reachable at all in
// a given run: a home-region-only type is not scanned from a non-home region.
func renderIndex(entries []indexEntry) string {
	var b strings.Builder

	b.WriteString("# Resource types\n\n")
	fmt.Fprintf(&b, "`oci-nuke` covers **%d** resource types. This page is generated from the\n", len(entries))
	b.WriteString("registry by `tools/generate-docs`, so it cannot fall behind the code.\n\n")
	b.WriteString("Use a name from the first column with `resource-types.includes` or\n")
	b.WriteString("`resource-types.excludes` in your config, and with `--only` on the command line.\n\n")

	scopes := make([]string, 0, len(entries))
	byScope := make(map[string][]indexEntry)
	for _, e := range entries {
		if _, seen := byScope[e.Scope]; !seen {
			scopes = append(scopes, e.Scope)
		}
		byScope[e.Scope] = append(byScope[e.Scope], e)
	}
	sort.Strings(scopes)

	for _, scope := range scopes {
		fmt.Fprintf(&b, "## Scope: `%s`\n\n", scope)
		b.WriteString("| Type | Notes |\n|---|---|\n")
		for _, e := range byScope[scope] {
			note := ""
			if e.AltResource != "" {
				note = fmt.Sprintf("alternative resource: `%s`", e.AltResource)
			}
			fmt.Fprintf(&b, "| [%s](%s.md) | %s |\n", e.Name, e.Name, note)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// loadPropertyConstants parses srcDir/support.go, if present, and returns every top-level
// `identifier = "value"` string constant it declares, keyed by identifier name. A srcDir with no
// support.go (a fixture-only srcDir, as this file's own unit tests use) returns an empty,
// non-nil map and no error -- resolving named constants is additive on top of literal-string
// extraction, never required.
func loadPropertyConstants(srcDir string) (map[string]string, error) {
	path := filepath.Join(srcDir, supportFileName)
	constants := make(map[string]string)

	if _, statErr := os.Stat(path); statErr != nil {
		if os.IsNotExist(statErr) {
			return constants, nil
		}
		return nil, fmt.Errorf("stat %s: %w", path, statErr)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vspec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, ident := range vspec.Names {
				if i >= len(vspec.Values) {
					continue // iota-style const with no per-line value; not this file's convention
				}
				if val, ok := stringLiteralValue(vspec.Values[i]); ok {
					constants[ident.Name] = val
				}
			}
		}
	}

	return constants, nil
}

// filenameOverrides maps a registered type Name to its actual snake_case source filename (minus
// the .go suffix), for the rare identifier whose most literal toSnakeCase conversion diverges
// from the real filename on disk. "MySQLDbSystem" is the first such case (05-02-PLAN.md):
// toSnakeCase's CamelCase-boundary rule derives "my_sql_db_system" (correctly splitting "My" from
// the "SQL" initialism by its own rule), but the real file, resources/mysql_db_system.go, treats
// "MySQL" as a single lowercase word, matching the OCI Go SDK's own `mysql` package name and
// resources/instance.go-style naming precedent. An override table entry here is preferred over
// further special-casing the general algorithm, which is correct for every other registered type.
var filenameOverrides = map[string]string{
	"MySQLDbSystem": "mysql_db_system",
}

// sourceFilePath locates name's source file under srcDir (srcDir/snake_case(name)+".go" -- the
// same convention cmd/gen-resource writes to, except for the rare filenameOverrides entry above),
// shared by propertyKeysForType and notesForType so the two never drift on which file a type's
// name resolves to.
func sourceFilePath(srcDir, name string) string {
	stem, ok := filenameOverrides[name]
	if !ok {
		stem = toSnakeCase(name)
	}
	return filepath.Join(srcDir, stem+".go")
}

// propertyKeysForType locates name's source file under srcDir, parses it, and returns
// extractPropertyKeys' result for its Properties() method.
func propertyKeysForType(srcDir, name string, constants map[string]string) ([]string, error) {
	path := sourceFilePath(srcDir, name)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	return extractPropertyKeys(f, constants), nil
}

// noteMarker is the paragraph-start marker generate-docs looks for inside a resource type's own
// GoDoc comment (the comment immediately preceding its `type <Name> struct { ... }` declaration)
// to extract free-form documentation this tool renders as a "## Notes" section in the generated
// doc. Moving hand-maintained prose into the type's own source doc comment -- rather than
// teaching this tool to merge into a previously-written docs/resources/<Type>.md file -- keeps
// DOC-04's own discipline intact end to end: every word of every generated doc, including this
// section, derives from resources/*.go source, never from a second, hand-edited copy that can
// silently drift out of sync with a regeneration. This is not hypothetical: `go run
// ./tools/generate-docs` previously rewrote docs/resources/LoadBalancer.md and
// NetworkLoadBalancer.md, silently deleting their hand-maintained RES-11 notes, because the
// generator had no mechanism to preserve manually appended sections
// (.planning/phases/06-compartment-delete-last/deferred-items.md, Plan 06-02).
const noteMarker = "Notes:"

// notesForType locates name's source file under srcDir (the same convention propertyKeysForType
// uses, via sourceFilePath) and returns the free-form Notes content in that type's own doc
// comment, if any -- see extractNotes for the exact marker convention. "" (with a nil error) is
// the common case: most types have nothing extra to document beyond their Scope/DependsOn/
// Properties.
func notesForType(srcDir, name string) (string, error) {
	path := sourceFilePath(srcDir, name)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.AllErrors)
	if err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}

	return extractNotes(f, name), nil
}

// extractNotes returns the free-form content following a noteMarker paragraph inside typeName's
// own doc comment in file -- everything from (and excluding) the marker itself to the end of the
// comment group, trimmed. Looks at the type's own TypeSpec.Doc first (the shape a grouped `type (
// ... )` block would use), falling back to the enclosing GenDecl.Doc (the shape every resource
// type in this codebase actually uses -- a standalone `type X struct { ... }` declaration, whose
// doc comment attaches to the GenDecl, not a per-spec TypeSpec.Doc). A type with no Notes:
// paragraph in either returns "".
func extractNotes(file *ast.File, typeName string) string {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			tspec, ok := spec.(*ast.TypeSpec)
			if !ok || tspec.Name.Name != typeName {
				continue
			}
			doc := tspec.Doc
			if doc == nil {
				doc = gen.Doc
			}
			if doc == nil {
				return ""
			}
			text := doc.Text()
			idx := strings.Index(text, noteMarker)
			if idx == -1 {
				return ""
			}
			return strings.TrimSpace(text[idx+len(noteMarker):])
		}
	}
	return ""
}

// recordPropertyKeysFromCall inspects a single call expression found inside a Properties() method
// body and, if it is a `.Set(...)`/`.SetTag(...)` call or a `baseProperties(...)` call, adds the
// key(s) it documents into keys. Split out of extractPropertyKeys to keep that function's
// cyclomatic complexity low (gocyclo) -- this is the only place call-shape branching happens.
func recordPropertyKeysFromCall(call *ast.CallExpr, constants map[string]string, keys map[string]struct{}) {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if fun.Sel.Name != "Set" && fun.Sel.Name != "SetTag" {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		if key, ok := stringLiteralValue(call.Args[0]); ok {
			keys[key] = struct{}{}
			return
		}
		if ident, ok := call.Args[0].(*ast.Ident); ok {
			if key, ok := constants[ident.Name]; ok {
				keys[key] = struct{}{}
			}
		}
	case *ast.Ident:
		if fun.Name == "baseProperties" {
			for _, k := range baseDocumentedKeys {
				keys[k] = struct{}{}
			}
		}
	}
}

// extractPropertyKeys walks file for a Properties() method (any receiver) and returns the
// sorted, deduplicated set of property keys that method's body documents:
//   - every string literal first argument to a .Set(...)/.SetTag(...) call
//   - every first argument that is a bare identifier resolvable via constants (the
//     resources/support.go propX convention -- e.g. Set(propAvailabilityDomain, ...) documents
//     "availability_domain" exactly as if the literal had been written inline), so a type
//     following support.go's own goconst-driven convention is never silently under-documented
//     (.SetTagWithPrefix(...) is deliberately skipped -- its key argument is a per-instance tag
//     name, not a static property name, per 04-RESEARCH.md Q6)
//   - if the method's body calls the shared baseProperties helper (resources/support.go), the
//     fixed baseDocumentedKeys baseline that helper documents
func extractPropertyKeys(file *ast.File, constants map[string]string) []string {
	keys := make(map[string]struct{})

	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "Properties" || fn.Body == nil {
			return true
		}

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				recordPropertyKeysFromCall(call, constants, keys)
			}
			return true
		})

		return false // Properties() found and walked fully above; no need to recurse into it again
	})

	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stringLiteralValue reports the unquoted string value of e, if e is a string literal.
func stringLiteralValue(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// renderDoc renders name's markdown doc: its Scope, its sorted DependsOn (or "none"), its sorted
// property key list (or a "(none documented)" placeholder), and -- only when notesForType found a
// Notes: paragraph in the type's own doc comment -- a trailing "## Notes" section, verbatim.
// Every list sorted, and the Notes section either fully present or fully absent (never a stale
// leftover from a previous run), so two runs against the same registration/source state produce
// byte-identical output.
func renderDoc(name string, reg *registry.Registration, keys []string, notes string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", name)
	fmt.Fprintf(&b, "- Scope: %s\n", reg.Scope)

	deps := "none"
	if len(reg.DependsOn) > 0 {
		sorted := append([]string(nil), reg.DependsOn...)
		sort.Strings(sorted)
		deps = strings.Join(sorted, ", ")
	}
	fmt.Fprintf(&b, "- DependsOn: %s\n\n", deps)

	b.WriteString("## Properties\n\n")
	if len(keys) == 0 {
		b.WriteString("(none documented)\n")
	} else {
		for _, k := range keys {
			fmt.Fprintf(&b, "- `%s`\n", k)
		}
	}

	if notes != "" {
		fmt.Fprintf(&b, "\n## Notes\n\n%s\n", notes)
	}

	return b.String()
}

// toSnakeCase converts a CamelCase identifier to snake_case -- the exact same rule
// cmd/gen-resource's main.go uses for the same purpose (kept as its own small copy here rather
// than a shared internal package, since neither tool imports the other and the rule is a handful
// of lines).
func toSnakeCase(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 {
			prevLower := unicode.IsLower(runes[i-1])
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			prevUpper := unicode.IsUpper(runes[i-1])
			if prevLower || (prevUpper && nextLower) {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
