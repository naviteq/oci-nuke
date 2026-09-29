package resources

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	liberrors "github.com/ekristen/libnuke/pkg/errors"
)

// conflictError is a full common.ServiceError whose code and message are settable --
// stubServiceError (container_repository_test.go) hard-codes both, and this file's whole subject
// is whether the API's own words reach item.Reason.
type conflictError struct {
	statusCode int
	code       string
	message    string
}

func (e *conflictError) Error() string           { return e.code + ": " + e.message }
func (e *conflictError) GetHTTPStatusCode() int  { return e.statusCode }
func (e *conflictError) GetMessage() string      { return e.message }
func (e *conflictError) GetCode() string         { return e.code }
func (e *conflictError) GetOpcRequestID() string { return "" }

func TestHoldOn409NilStaysNil(t *testing.T) {
	t.Parallel()

	if err := holdOn409(nil); err != nil {
		t.Fatalf("holdOn409(nil) = %v, want nil", err)
	}
}

func TestHoldOn409CarriesTheAPIsOwnWords(t *testing.T) {
	t.Parallel()

	err := holdOn409(&conflictError{
		statusCode: http.StatusConflict,
		code:       "BucketNotEmpty",
		message:    "The bucket is not empty. Delete all objects and try again.",
	})

	// ErrHoldResource and not ErrWaitResource: HandleRemove (libnuke pkg/nuke/nuke.go:606) maps
	// only ErrHoldResource to ItemStateHold, and Hold is the only non-terminal state
	// HandleWaitDependency counts.
	var hold liberrors.ErrHoldResource
	if !errors.As(err, &hold) {
		t.Fatalf("holdOn409 on a 409 = %T(%v), want liberrors.ErrHoldResource", err, err)
	}

	const want = "HTTP 409 BucketNotEmpty: The bucket is not empty. Delete all objects and try again."
	if hold.Error() != want {
		t.Errorf("hold reason = %q, want %q", hold.Error(), want)
	}
}

func TestHoldOn409NeverReturnsErrWaitResource(t *testing.T) {
	t.Parallel()

	// ErrWaitResource returned from Remove() is not matched by HandleRemove at all and falls
	// through to ItemStateFailed -- the state that stops blocking dependents, which is the
	// defect NR-787 is about. NR-787's own analysis named this error type; it is the wrong one.
	err := holdOn409(&conflictError{statusCode: http.StatusConflict, code: "IncorrectState"})

	var wait liberrors.ErrWaitResource
	if errors.As(err, &wait) {
		t.Fatalf("holdOn409 returned ErrWaitResource(%q); HandleRemove ignores that type", wait.Error())
	}
}

func TestHoldOn409WithoutCodeOrMessage(t *testing.T) {
	t.Parallel()

	err := holdOn409(&conflictError{statusCode: http.StatusConflict})

	var hold liberrors.ErrHoldResource
	if !errors.As(err, &hold) {
		t.Fatalf("holdOn409 on a bare 409 = %T(%v), want liberrors.ErrHoldResource", err, err)
	}
	if hold.Error() != "HTTP 409" {
		t.Errorf("hold reason = %q, want %q", hold.Error(), "HTTP 409")
	}
}

func TestHoldOn409LeavesEveryOtherStatusTerminal(t *testing.T) {
	t.Parallel()

	// 429 and 5xx included on purpose: oci-go-sdk's DefaultRetryPolicy has already retried and
	// abandoned those beneath us, so holding on them would multiply wall-clock against a failure
	// the SDK already called final.
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusServiceUnavailable,
	} {
		original := &conflictError{statusCode: status, code: "Whatever"}
		returned := holdOn409(original)

		var hold liberrors.ErrHoldResource
		if errors.As(returned, &hold) {
			t.Errorf("status %d became ErrHoldResource; only 409 may hold", status)
		}
		if !errors.Is(returned, error(original)) {
			t.Errorf("status %d: holdOn409 must return the error unwrapped, got %v", status, returned)
		}
	}
}

func TestHoldOn409PassesANonServiceErrorThrough(t *testing.T) {
	t.Parallel()

	original := errors.New("dial tcp: connection refused")
	if returned := holdOn409(original); !errors.Is(returned, original) {
		t.Errorf("holdOn409(%v) = %v, want the same error", original, returned)
	}
}

// containerRepositoryFile is the one documented exception -- its 409 means "not until the images
// are gone", which cannot clear inside a run, so holding on it would spend the whole
// MaxWaitRetries budget to reach the report the terminal path gives immediately.
const containerRepositoryFile = "container_repository.go"

// bareErrReturns counts `return err` statements -- the pre-fix shape -- inside every Remove()
// method declared in one source file.
func bareErrReturns(t *testing.T, file string) (removes, bare int) {
	t.Helper()

	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}

	parsed, err := parser.ParseFile(token.NewFileSet(), file, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Remove" || fn.Recv == nil || fn.Body == nil {
			continue
		}
		removes++

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			ret, isReturn := n.(*ast.ReturnStmt)
			if !isReturn || len(ret.Results) != 1 {
				return true
			}
			if ident, isIdent := ret.Results[0].(*ast.Ident); isIdent && ident.Name == "err" {
				bare++
			}
			return true
		})
	}

	return removes, bare
}

// TestEveryRemoveRoutesThroughHoldOn409 is the guard that matters more than the cases above. The
// defect was one Remove() returning a raw error; a resource file added later with a plain
// `return err` reintroduces exactly that, and nothing else in the suite would notice.
func TestEveryRemoveRoutesThroughHoldOn409(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob this package's sources: %v", err)
	}

	inspected := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		removes, bare := bareErrReturns(t, file)
		inspected += removes

		switch {
		case file == containerRepositoryFile:
			if removes > 0 && bare == 0 {
				t.Errorf("%s is recorded as the documented exception, but no longer returns a "+
					"bare err; route it through holdOn409 and drop the exception", file)
			}
		case bare != 0:
			t.Errorf("%s: Remove() returns a bare err %d time(s). An OCI 409 must go through "+
				"holdOn409, or the item goes terminal-failed and stops blocking whatever "+
				"DependsOn it (NR-787)", file, bare)
		}
	}

	if inspected <= 50 {
		t.Errorf("inspected %d Remove() implementations; this package has one per resource type, "+
			"so this guard is probably no longer looking where the resources are", inspected)
	}
}
