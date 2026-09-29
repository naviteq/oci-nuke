// Command oci-nuke-gen is the scaffold generator RES-01 requires: "adding a resource type
// requires implementing one lister and one resource struct against a documented framework." A
// single invocation renders resources/{snake_case_name}.go and its
// resources/{snake_case_name}_test.go from the exact template Plan 04-01/04-02 established --
// narrow SDK client interface, dual-meaning Filter() lifecycle switch, baseProperties,
// SafetyTags (ocinuke.SafetyEvaluated, scan-time protect-by-tag/min-age), CurrentScope/
// CurrentReporter -- parameterized on the target resource type's SDK method/field names via
// flags.
//
// Usage:
//
//	oci-nuke-gen resource \
//	  --name NatGateway \
//	  --sdk-package core --sdk-type NatGateway --client-accessor VirtualNetwork \
//	  --list-method ListNatGateways --list-request-field CompartmentId \
//	  --delete-method DeleteNatGateway --delete-id-field NatGatewayId \
//	  --lifecycle-field LifecycleState --lifecycle-present Available \
//	  --depends-on ""
package main

import (
	"embed"
	"flag"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"
)

//go:embed template.go.tmpl
var templateFS embed.FS

// stringSliceFlag collects a repeatable flag's values in the order given, e.g.
// --lifecycle-present Available --lifecycle-present Updating for a type with more than one
// actionable/present lifecycle state.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }

func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// templateData carries every value template.go.tmpl's "resource" and "test" templates need,
// combining flag values verbatim with names derived from them (interface/lister/const names,
// the fully-qualified lifecycle "present" constants, and the isolated list-helper function name).
type templateData struct {
	Name                string
	SDKPackage          string
	SDKType             string
	ClientAccessor      string
	ClientInterfaceName string
	ListerTypeName      string
	ListHelperName      string
	ResourceTypeConst   string
	FieldName           string
	ListMethod          string
	ListRequestField    string
	DeleteMethod        string
	DeleteIDField       string
	LifecycleField      string
	LifecycleEnumType   string

	// LifecyclePresentConsts holds one fully-qualified SDK constant reference per
	// --lifecycle-present value, e.g. "core.NatGatewayLifecycleStateAvailable" -- computed once
	// here so neither template needs to know the "<SDKType><LifecycleField><Value>" naming
	// convention itself.
	LifecyclePresentConsts []string

	DependsOn []string

	// ExtraProperties holds one entry per --extra-property flag, rendered as an additional
	// .Set(KeyExpr, x.Field) chained onto Properties()'s baseProperties(...) call -- for SDK
	// fields baseProperties itself does not cover (e.g. NatGateway's VcnId/BlockTraffic). See
	// extraProperty's own doc comment for how KeyExpr is derived.
	ExtraProperties []extraProperty
}

// extraProperty is one --extra-property flag value, parsed by parseExtraProperties.
type extraProperty struct {
	// KeyExpr is the Go expression the template emits as .Set(KeyExpr, ...)'s first argument --
	// either a bare identifier naming an existing resources/support.go propX constant (when the
	// property key matches one in wellKnownPropertyConstants, so this file reuses the SAME shared
	// constant every other resources/*.go file already reuses for a 3+-occurrence key, keeping the
	// generated file goconst-clean by construction) or a quoted string literal otherwise.
	KeyExpr string
	// Field is the SDK struct field name to read the property's value from, e.g. "VcnId".
	Field string
}

// wellKnownPropertyConstants maps a --extra-property key to the resources/support.go constant
// identifier that already declares it, mirroring that file's own const block exactly. A key not
// present here is rendered as its own quoted string literal instead -- correct for a property
// that is not (yet) shared 3+ times across resources/*.go, per support.go's own documented
// goconst threshold.
var wellKnownPropertyConstants = map[string]string{
	"vcn_id":              "propVcnID",
	"subnet_id":           "propSubnetID",
	"instance_id":         "propInstanceID",
	"volume_id":           "propVolumeID",
	"file_system_id":      "propFileSystemID",
	"availability_domain": "propAvailabilityDomain",
	"size_in_gbs":         "propSizeInGBs",
	"namespace":           "propNamespace",
	"bucket":              "propBucket",
	"export_set_id":       "propExportSetID",
}

// parseExtraProperties turns each "key=Field" --extra-property flag value into an extraProperty,
// resolving key against wellKnownPropertyConstants where possible.
func parseExtraProperties(raw []string) ([]extraProperty, error) {
	out := make([]extraProperty, 0, len(raw))
	for _, r := range raw {
		key, field, ok := strings.Cut(r, "=")
		if !ok || key == "" || field == "" {
			return nil, fmt.Errorf("--extra-property %q: want KEY=SDKField", r)
		}
		keyExpr := fmt.Sprintf("%q", key)
		if constName, known := wellKnownPropertyConstants[key]; known {
			keyExpr = constName
		}
		out = append(out, extraProperty{KeyExpr: keyExpr, Field: field})
	}
	return out, nil
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "resource" {
		fmt.Fprintln(os.Stderr, "usage: oci-nuke-gen resource --name <Name> [flags]")
		os.Exit(2)
	}

	if err := runResource(os.Args[2:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "oci-nuke-gen resource:", err)
		os.Exit(1)
	}
}

// runResource parses the "resource" subcommand's flags, renders both output files, and writes
// them under outDir (default "resources", overridable via --out-dir so this generator's own
// regression test -- and this plan's golden-file generation -- never has to leave a real
// registration behind in resources/).
func runResource(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("resource", flag.ContinueOnError)

	name := fs.String("name", "", "resource type name, e.g. NatGateway (required)")
	sdkPackage := fs.String("sdk-package", "", "OCI SDK subpackage import name, e.g. core (required)")
	sdkType := fs.String("sdk-type", "", "OCI SDK struct type name, e.g. NatGateway (required)")
	clientAccessor := fs.String("client-accessor", "", "pkg/clients.Cache method name, e.g. VirtualNetwork (required)")
	listMethod := fs.String("list-method", "", "SDK list method name, e.g. ListNatGateways (required)")
	listRequestField := fs.String("list-request-field", "", "list request struct's compartment field name, e.g. CompartmentId (required)")
	deleteMethod := fs.String("delete-method", "", "SDK delete method name, e.g. DeleteNatGateway (required)")
	deleteIDField := fs.String("delete-id-field", "", "delete request struct's id field name, e.g. NatGatewayId (required)")
	lifecycleField := fs.String("lifecycle-field", "", "lifecycle-state struct field name, e.g. LifecycleState (required)")
	dependsOn := fs.String("depends-on", "", "comma-separated resource type names this type depends on")
	outDir := fs.String("out-dir", "resources", "directory the generated files are written to")
	var lifecyclePresent stringSliceFlag
	fs.Var(&lifecyclePresent, "lifecycle-present",
		"an actionable/present lifecycle-state value suffix, e.g. Available (repeatable for multi-state \"present\"; required at least once)")
	var extraPropertyFlag stringSliceFlag
	fs.Var(&extraPropertyFlag, "extra-property",
		"an extra Properties() entry beyond baseProperties, as KEY=SDKField, e.g. vcn_id=VcnId (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	data, err := newTemplateData(&templateDataInput{
		Name:             *name,
		SDKPackage:       *sdkPackage,
		SDKType:          *sdkType,
		ClientAccessor:   *clientAccessor,
		ListMethod:       *listMethod,
		ListRequestField: *listRequestField,
		DeleteMethod:     *deleteMethod,
		DeleteIDField:    *deleteIDField,
		LifecycleField:   *lifecycleField,
		LifecyclePresent: lifecyclePresent,
		DependsOn:        *dependsOn,
		ExtraProperty:    extraPropertyFlag,
	})
	if err != nil {
		return err
	}

	resourceSrc, err := renderAndFormat("resource", &data)
	if err != nil {
		return fmt.Errorf("rendering resource file: %w", err)
	}
	testSrc, err := renderAndFormat("test", &data)
	if err != nil {
		return fmt.Errorf("rendering test file: %w", err)
	}

	snake := toSnakeCase(data.Name)
	resourcePath := filepath.Join(*outDir, snake+".go")
	testPath := filepath.Join(*outDir, snake+"_test.go")

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", *outDir, err)
	}
	if err := os.WriteFile(resourcePath, resourceSrc, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", resourcePath, err)
	}
	if err := os.WriteFile(testPath, testSrc, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", testPath, err)
	}

	fmt.Fprintf(stdout, "wrote %s\n", resourcePath)
	fmt.Fprintf(stdout, "wrote %s\n", testPath)
	return nil
}

// templateDataInput is the raw, unvalidated flag input newTemplateData turns into a fully
// derived templateData -- kept as its own type so runResource's flag parsing stays separate from
// the (independently testable) derivation logic.
type templateDataInput struct {
	Name             string
	SDKPackage       string
	SDKType          string
	ClientAccessor   string
	ListMethod       string
	ListRequestField string
	DeleteMethod     string
	DeleteIDField    string
	LifecycleField   string
	LifecyclePresent []string
	DependsOn        string
	ExtraProperty    []string
}

// newTemplateData validates in.required fields are non-empty and derives every name
// template.go.tmpl needs from them.
func newTemplateData(in *templateDataInput) (templateData, error) {
	required := map[string]string{
		"--name":               in.Name,
		"--sdk-package":        in.SDKPackage,
		"--sdk-type":           in.SDKType,
		"--client-accessor":    in.ClientAccessor,
		"--list-method":        in.ListMethod,
		"--list-request-field": in.ListRequestField,
		"--delete-method":      in.DeleteMethod,
		"--delete-id-field":    in.DeleteIDField,
		"--lifecycle-field":    in.LifecycleField,
	}
	for flagName, v := range required {
		if v == "" {
			return templateData{}, fmt.Errorf("%s is required", flagName)
		}
	}
	if len(in.LifecyclePresent) == 0 {
		return templateData{}, fmt.Errorf("--lifecycle-present is required at least once")
	}

	var dependsOn []string
	for _, d := range strings.Split(in.DependsOn, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			dependsOn = append(dependsOn, d)
		}
	}

	lifecycleEnumType := in.SDKType + in.LifecycleField + "Enum"
	presentConsts := make([]string, 0, len(in.LifecyclePresent))
	for _, v := range in.LifecyclePresent {
		presentConsts = append(presentConsts, fmt.Sprintf("%s.%s%s%s", in.SDKPackage, in.SDKType, in.LifecycleField, v))
	}

	extraProperties, err := parseExtraProperties(in.ExtraProperty)
	if err != nil {
		return templateData{}, err
	}

	return templateData{
		Name:                   in.Name,
		SDKPackage:             in.SDKPackage,
		SDKType:                in.SDKType,
		ClientAccessor:         in.ClientAccessor,
		ClientInterfaceName:    lowerFirst(in.Name) + "Client",
		ListerTypeName:         lowerFirst(in.Name) + "Lister",
		ListHelperName:         lowerFirst(in.Name) + "List",
		ResourceTypeConst:      in.Name + "ResourceType",
		FieldName:              lowerFirst(in.Name),
		ListMethod:             in.ListMethod,
		ListRequestField:       in.ListRequestField,
		DeleteMethod:           in.DeleteMethod,
		DeleteIDField:          in.DeleteIDField,
		LifecycleField:         in.LifecycleField,
		LifecycleEnumType:      lifecycleEnumType,
		LifecyclePresentConsts: presentConsts,
		DependsOn:              dependsOn,
		ExtraProperties:        extraProperties,
	}, nil
}

// renderAndFormat executes the named ("resource" or "test") template against data and formats
// the result via go/format.Source -- exact template whitespace is irrelevant; the formatted
// output is what gets written to disk and what the golden-file test compares byte-for-byte.
func renderAndFormat(name string, data *templateData) ([]byte, error) {
	tmpl, err := template.New("template.go.tmpl").Funcs(template.FuncMap{
		"join": strings.Join,
	}).ParseFS(templateFS, "template.go.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parsing embedded template: %w", err)
	}

	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return nil, fmt.Errorf("executing %q template: %w", name, err)
	}

	formatted, err := format.Source([]byte(buf.String()))
	if err != nil {
		return nil, fmt.Errorf("formatting generated %q source: %w\n---\n%s", name, err, buf.String())
	}
	return formatted, nil
}

// lowerFirst lowercases s's first rune, leaving the rest unchanged -- "NatGateway" -> "natGateway".
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// toSnakeCase converts a CamelCase identifier to snake_case, matching the file-naming
// convention every resources/*.go file follows ("NatGateway" -> "nat_gateway"). An uppercase
// rune gets a preceding underscore when the previous rune is lowercase (a new word starting) or
// when the previous rune is uppercase but the next rune is lowercase (the end of an acronym run,
// e.g. "ID" followed by "Value" in "IDValue" -> "id_value").
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
