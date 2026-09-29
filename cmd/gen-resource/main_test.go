package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testResourceName is the resource type name (and matching SDK type name) every test in this
// file exercises -- 04-RESEARCH.md Q1's worked NatGateway example, parameterized. Declared once
// so the literal is never repeated (goconst, min-occurrences: 3).
const testResourceName = "NatGateway"

// lifecyclePresentAvailable is the "Available" lifecycle-present flag value, shared across this
// file and dogfood_test.go so the literal is never repeated (goconst, min-occurrences: 3).
const lifecyclePresentAvailable = "Available"

// natGatewayInput is the exact NatGateway invocation this project's scaffold generator's golden
// file (testdata/nat_gateway.go.golden) was generated from. Every test in this file that needs a
// real, valid input starts here.
func natGatewayInput() templateDataInput {
	return templateDataInput{
		Name:             testResourceName,
		SDKPackage:       "core",
		SDKType:          testResourceName,
		ClientAccessor:   "VirtualNetwork",
		ListMethod:       "ListNatGateways",
		ListRequestField: "CompartmentId",
		DeleteMethod:     "DeleteNatGateway",
		DeleteIDField:    "NatGatewayId",
		LifecycleField:   "LifecycleState",
		LifecyclePresent: []string{lifecyclePresentAvailable},
		DependsOn:        "",
	}
}

// TestRenderAndFormat_MatchesGoldenFile is T-04-08's mitigation: it proves the exact emitted
// Filter() body (and everything else in the resource file) matches the documented allow-list-
// of-"present" pattern byte-for-byte against a checked-in golden file. Any future template edit
// that changes this shape fails this diff, not merely a human review.
func TestRenderAndFormat_MatchesGoldenFile(t *testing.T) {
	in := natGatewayInput()
	data, err := newTemplateData(&in)
	if err != nil {
		t.Fatalf("newTemplateData() error = %v", err)
	}

	got, err := renderAndFormat("resource", &data)
	if err != nil {
		t.Fatalf("renderAndFormat(resource) error = %v", err)
	}

	want, err := os.ReadFile(filepath.Join("testdata", "nat_gateway.go.golden"))
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"rendered resource file does not match testdata/nat_gateway.go.golden byte-for-byte\n"+
				"--- got ---\n%s\n--- want ---\n%s",
			got, want,
		)
	}
}

// TestRenderAndFormat_CompilesCleanly renders both the resource file and its test skeleton,
// writes them alongside a copy of resources/support.go into a scratch package nested inside this
// module (so import paths like github.com/naviteq/oci-nuke/pkg/ocinuke still resolve), and
// runs `go vet` over it. A template bug that emits malformed or non-type-checking Go is caught
// here, not discovered the first time a human runs the real command.
func TestRenderAndFormat_CompilesCleanly(t *testing.T) {
	in := natGatewayInput()
	data, err := newTemplateData(&in)
	if err != nil {
		t.Fatalf("newTemplateData() error = %v", err)
	}

	resourceSrc, err := renderAndFormat("resource", &data)
	if err != nil {
		t.Fatalf("renderAndFormat(resource) error = %v", err)
	}
	testSrc, err := renderAndFormat("test", &data)
	if err != nil {
		t.Fatalf("renderAndFormat(test) error = %v", err)
	}
	supportSrc, err := os.ReadFile(filepath.Join("..", "..", "resources", "support.go"))
	if err != nil {
		t.Fatalf("reading resources/support.go: %v", err)
	}
	// The template's Remove() calls holdOn409, so the scratch package needs it to compile --
	// the same reason support.go is copied in.
	removeErrSrc, err := os.ReadFile(filepath.Join("..", "..", "resources", "remove_error.go"))
	if err != nil {
		t.Fatalf("reading resources/remove_error.go: %v", err)
	}

	// Nested under this package's own directory (not t.TempDir(), which defaults to the OS temp
	// dir outside the module) so `go vet` resolves this module's go.mod by walking up from here.
	dir, err := os.MkdirTemp(".", "buildcheck-")
	if err != nil {
		t.Fatalf("creating scratch build dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	for name, content := range map[string][]byte{
		"nat_gateway.go":      resourceSrc,
		"nat_gateway_test.go": testSrc,
		"support.go":          supportSrc,
		"remove_error.go":     removeErrSrc,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	// dir is this test's own os.MkdirTemp output, never external/user-controlled input --
	// gosec's G204 (subprocess launched with variable argument) cannot distinguish that from
	// tainted input by construction, so it is documented here rather than restructured away, the
	// same way this project already documents other structurally-forced findings (see
	// pkg/ocinuke/runcontext.go's CurrentReporter and pkg/scope/tree_test.go's
	// fakeIdentityClient).
	//nolint:gosec // dir is this test's own os.MkdirTemp output, not external input
	cmd := exec.CommandContext(context.Background(), "go", "vet", "./"+filepath.Base(dir)+"/...")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go vet on generated files failed: %v\n%s", err, out)
	}
}

// TestNewTemplateData_DependsOnEmitsBareStringLiterals proves --depends-on
// "Subnet,RouteTable" produces a DependsOn: []string{"Subnet", "RouteTable"} literal -- bare
// strings, per this wave's convention of declaring cross-type DependsOn edges as plain string
// literals rather than importing another file's exported constant (04-RESEARCH.md Q3/Q9), so
// generated/hand-written files across different domain plans never need to compile against each
// other.
func TestNewTemplateData_DependsOnEmitsBareStringLiterals(t *testing.T) {
	in := natGatewayInput()
	in.DependsOn = "Subnet,RouteTable"

	data, err := newTemplateData(&in)
	if err != nil {
		t.Fatalf("newTemplateData() error = %v", err)
	}

	got, err := renderAndFormat("resource", &data)
	if err != nil {
		t.Fatalf("renderAndFormat(resource) error = %v", err)
	}

	const want = `DependsOn: []string{"Subnet", "RouteTable"}`
	if !strings.Contains(string(got), want) {
		t.Fatalf("rendered resource file does not contain %q\n--- got ---\n%s", want, got)
	}
}

// TestNewTemplateData_RequiresEveryFlag proves every documented required flag is actually
// enforced -- a template rendered against a zero-value field (e.g. an empty --sdk-package)
// would silently emit malformed Go rather than failing fast with a clear error.
func TestNewTemplateData_RequiresEveryFlag(t *testing.T) {
	base := natGatewayInput()

	cases := []struct {
		name   string
		mutate func(in *templateDataInput)
	}{
		{"missing name", func(in *templateDataInput) { in.Name = "" }},
		{"missing sdk-package", func(in *templateDataInput) { in.SDKPackage = "" }},
		{"missing sdk-type", func(in *templateDataInput) { in.SDKType = "" }},
		{"missing client-accessor", func(in *templateDataInput) { in.ClientAccessor = "" }},
		{"missing list-method", func(in *templateDataInput) { in.ListMethod = "" }},
		{"missing list-request-field", func(in *templateDataInput) { in.ListRequestField = "" }},
		{"missing delete-method", func(in *templateDataInput) { in.DeleteMethod = "" }},
		{"missing delete-id-field", func(in *templateDataInput) { in.DeleteIDField = "" }},
		{"missing lifecycle-field", func(in *templateDataInput) { in.LifecycleField = "" }},
		{"missing lifecycle-present", func(in *templateDataInput) { in.LifecyclePresent = nil }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mutate(&in)
			if _, err := newTemplateData(&in); err == nil {
				t.Fatalf("newTemplateData() with %s = nil error, want non-nil", tc.name)
			}
		})
	}
}

func TestLowerFirst(t *testing.T) {
	cases := map[string]string{
		"":               "",
		testResourceName: "natGateway",
		"Vcn":            "vcn",
		"a":              "a",
	}
	for in, want := range cases {
		if got := lowerFirst(in); got != want {
			t.Errorf("lowerFirst(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToSnakeCase(t *testing.T) {
	cases := map[string]string{
		testResourceName:  "nat_gateway",
		"Vcn":             "vcn",
		"DedicatedVmHost": "dedicated_vm_host",
		"LoadBalancer":    "load_balancer",
		"Instance":        "instance",
		"NetworkSecurity": "network_security",
	}
	for in, want := range cases {
		if got := toSnakeCase(in); got != want {
			t.Errorf("toSnakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRunResource_WritesFilesUnderOutDir proves the full CLI path (flag parsing through file
// writing) produces the two expected files under a caller-chosen --out-dir, without ever
// touching the real resources/ package -- this is what lets this test suite (and this plan's own
// golden-file generation) run repeatedly without leaving a stray registration behind.
func TestRunResource_WritesFilesUnderOutDir(t *testing.T) {
	dir, err := os.MkdirTemp(".", "outdir-")
	if err != nil {
		t.Fatalf("creating scratch out dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	var stdout strings.Builder
	args := []string{
		"--name", testResourceName,
		"--sdk-package", "core",
		"--sdk-type", testResourceName,
		"--client-accessor", "VirtualNetwork",
		"--list-method", "ListNatGateways",
		"--list-request-field", "CompartmentId",
		"--delete-method", "DeleteNatGateway",
		"--delete-id-field", "NatGatewayId",
		"--lifecycle-field", "LifecycleState",
		"--lifecycle-present", lifecyclePresentAvailable,
		"--depends-on", "",
		"--out-dir", dir,
	}

	if err := runResource(args, &stdout); err != nil {
		t.Fatalf("runResource() error = %v", err)
	}

	for _, name := range []string{"nat_gateway.go", "nat_gateway_test.go"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
		}
	}
}
