// Command generate-config-docs renders pkg/config/schema/config.schema.json as a Markdown
// reference and writes it between the marker comments in docs/configuration.md. The schema is
// the authority for `config validate`, so it is also the authority for what the documentation
// says a config may contain -- a transcribed table is wrong the first time a field is added.
//
// Prose lives outside the markers and is hand-written: the schema carries no descriptions, so a
// fully generated page would be a constraint table with nothing explaining why a field exists.
// The split is deliberate -- generate what drifts, write what does not.
//
// With -check the command writes nothing and exits non-zero if the file is out of date, which is
// what CI runs.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	defaultSchemaPath = "pkg/config/schema/config.schema.json"
	defaultDocPath    = "docs/configuration.md"

	beginMarker = "<!-- generated: config-schema -->"
	endMarker   = "<!-- /generated: config-schema -->"
)

func main() {
	check := flag.Bool("check", false, "exit non-zero if the doc is out of date instead of writing it")
	schemaPath := flag.String("schema", defaultSchemaPath, "path to the JSON Schema")
	docPath := flag.String("doc", defaultDocPath, "path to the Markdown file carrying the markers")
	flag.Parse()

	if err := run(*schemaPath, *docPath, *check); err != nil {
		fmt.Fprintln(os.Stderr, "generate-config-docs:", err)
		os.Exit(1)
	}
}

func run(schemaPath, docPath string, check bool) error {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", schemaPath, err)
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("parsing %s: %w", schemaPath, err)
	}

	doc, err := os.ReadFile(docPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", docPath, err)
	}

	updated, err := splice(string(doc), render(schema))
	if err != nil {
		return fmt.Errorf("%s: %w", docPath, err)
	}

	if check {
		if updated != string(doc) {
			return fmt.Errorf("%s is out of date; run `go run ./tools/generate-config-docs`", docPath)
		}
		return nil
	}

	// docPath is a flag on a developer-run generator, not untrusted input.
	return os.WriteFile(docPath, []byte(updated), 0o600) //nolint:gosec // flag-supplied path
}

// splice replaces the content between the markers, leaving everything outside them untouched.
// A missing marker is an error rather than an append: silently growing the file would produce
// two reference sections that disagree.
func splice(doc, generated string) (string, error) {
	begin := strings.Index(doc, beginMarker)
	if begin < 0 {
		return "", fmt.Errorf("marker %q not found", beginMarker)
	}
	end := strings.Index(doc, endMarker)
	if end < 0 {
		return "", fmt.Errorf("marker %q not found", endMarker)
	}
	if end < begin {
		return "", fmt.Errorf("marker %q appears before %q", endMarker, beginMarker)
	}
	return doc[:begin+len(beginMarker)] + "\n\n" + generated + "\n" + doc[end:], nil
}

func render(schema map[string]any) string {
	var b strings.Builder

	required := stringSet(schema["required"])
	props, _ := schema["properties"].(map[string]any)

	b.WriteString("### Top-level keys\n\n")
	renderPropertyTable(&b, props, required)

	defs, _ := schema["$defs"].(map[string]any)

	// Top-level keys whose shape is spelled inline rather than as a $def -- `resource-types`
	// and `settings` -- would otherwise show as a bare "object" with nowhere to look.
	inline := make([]string, 0, len(props))
	for _, name := range sortedKeys(props) {
		field, _ := props[name].(map[string]any)
		if sub, ok := field["properties"].(map[string]any); ok && len(sub) > 0 {
			inline = append(inline, name)
		}
	}

	if len(inline) == 0 && len(defs) == 0 {
		return b.String()
	}

	b.WriteString("\n### Nested objects\n")

	for _, name := range inline {
		field, _ := props[name].(map[string]any)
		sub, _ := field["properties"].(map[string]any)
		fmt.Fprintf(&b, "\n#### `%s`\n\n", name)
		renderPropertyTable(&b, sub, stringSet(field["required"]))
	}

	for _, defName := range sortedKeys(defs) {
		def, _ := defs[defName].(map[string]any)
		defProps, _ := def["properties"].(map[string]any)
		defRequired := stringSet(def["required"])

		fmt.Fprintf(&b, "\n#### `%s`\n\n", defName)

		// A oneOf def accepts several shapes -- `filter` is either a bare string or a full
		// object -- and collapsing that to a single table would misreport the string form as
		// unsupported. Each branch gets its own heading instead.
		if branches, ok := def["oneOf"].([]any); ok {
			renderBranches(&b, branches)
			continue
		}

		if len(defProps) == 0 {
			fmt.Fprintf(&b, "%s.", singleLine(typeOf(def)))
			if c := singleLine(constraintsOf(def)); c != "—" {
				fmt.Fprintf(&b, " %s.", c)
			}
			b.WriteString("\n")
			continue
		}
		renderPropertyTable(&b, defProps, defRequired)
	}

	return b.String()
}

// renderPropertyTable writes one row per key of an object shape.
func renderPropertyTable(b *strings.Builder, props map[string]any, required map[string]bool) {
	b.WriteString("| Key | Type | Required | Constraints |\n|---|---|---|---|\n")
	for _, name := range sortedKeys(props) {
		field, _ := props[name].(map[string]any)
		req := "no"
		if required[name] {
			req = "**yes**"
		}
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", name, typeOf(field), req, constraintsOf(field))
	}
}

// renderBranches writes one section per accepted shape of a oneOf.
func renderBranches(b *strings.Builder, branches []any) {
	fmt.Fprintf(b, "Accepts %d shapes.\n", len(branches))
	for _, raw := range branches {
		branch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, hasProps := branch["properties"].(map[string]any)
		if !hasProps || len(props) == 0 {
			fmt.Fprintf(b, "\n**As a %s.**", typeOf(branch))
			if c := singleLine(constraintsOf(branch)); c != "—" {
				fmt.Fprintf(b, " %s", c)
			}
			b.WriteString("\n")
			continue
		}
		fmt.Fprintf(b, "\n**As an object.**\n\n")
		renderPropertyTable(b, props, stringSet(branch["required"]))
	}
}

// typeOf names the shape a value must have, resolving $ref and array item types so the table
// reads as the YAML author experiences it rather than as JSON Schema spells it.
func typeOf(field map[string]any) string {
	if field == nil {
		return "any"
	}
	if ref, ok := field["$ref"].(string); ok {
		return refName(ref)
	}
	t, _ := field["type"].(string)
	switch t {
	case "array":
		items, _ := field["items"].(map[string]any)
		return "list of " + typeOf(items)
	case "object":
		if ap, ok := field["additionalProperties"].(map[string]any); ok {
			return "map of " + typeOf(ap)
		}
		if props, ok := field["properties"].(map[string]any); ok && len(props) > 0 {
			return "object (see below)"
		}
		return "object"
	case "":
		return "any"
	default:
		return t
	}
}

func refName(ref string) string {
	return "`" + ref[strings.LastIndex(ref, "/")+1:] + "`"
}

// constraintsOf reports only what the schema actually enforces. An empty cell means the schema
// constrains nothing beyond the type -- said with an em dash rather than left blank, so a
// missing constraint is distinguishable from a rendering bug.
func constraintsOf(field map[string]any) string {
	if field == nil {
		return "—"
	}
	var parts []string
	if p, ok := field["pattern"].(string); ok {
		parts = append(parts, "matches `"+p+"`")
	}
	if n, ok := field["minItems"].(float64); ok {
		parts = append(parts, fmt.Sprintf("at least %d entry", int(n)))
	}
	if bounds := numericBounds(field); bounds != "" {
		parts = append(parts, bounds)
	}
	if e, ok := field["enum"].([]any); ok {
		vals := make([]string, 0, len(e))
		for _, v := range e {
			vals = append(vals, fmt.Sprintf("`%v`", v))
		}
		parts = append(parts, "one of "+strings.Join(vals, ", "))
	}
	if d, ok := field["default"]; ok {
		parts = append(parts, fmt.Sprintf("default `%v`", d))
	}
	if ap, ok := field["additionalProperties"].(bool); ok && !ap {
		parts = append(parts, "no other keys allowed")
	}
	if items, ok := field["items"].(map[string]any); ok {
		if inner := constraintsOf(items); inner != "—" {
			parts = append(parts, "each entry "+inner)
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, "; ")
}

// numericBounds renders minimum/maximum as one phrase, or "" when the schema sets neither.
func numericBounds(field map[string]any) string {
	lo, hasLo := field["minimum"].(float64)
	hi, hasHi := field["maximum"].(float64)
	switch {
	case hasLo && hasHi:
		return fmt.Sprintf("between %d and %d", int(lo), int(hi))
	case hasLo:
		return fmt.Sprintf("at least %d", int(lo))
	case hasHi:
		return fmt.Sprintf("at most %d", int(hi))
	default:
		return ""
	}
}

// singleLine collapses whitespace runs so a constraint string cannot break a Markdown table row.
func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func stringSet(v any) map[string]bool {
	out := make(map[string]bool)
	list, ok := v.([]any)
	if !ok {
		return out
	}
	for _, item := range list {
		if s, ok := item.(string); ok {
			out[s] = true
		}
	}
	return out
}
