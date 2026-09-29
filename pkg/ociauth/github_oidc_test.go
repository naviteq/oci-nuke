package ociauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchGitHubOIDCToken_MissingEnvVars proves the missing-env-var case fails fast with a
// named error identifying both env vars and the likely missing job permission, mirroring
// AUTH-05's "report the underlying error" convention rather than a generic auth failure.
func TestFetchGitHubOIDCToken_MissingEnvVars(t *testing.T) {
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	_, err := fetchGitHubOIDCToken(context.Background(), "")
	if err == nil {
		t.Fatal("expected an error when both env vars are unset")
	}
	if !strings.Contains(err.Error(), "ACTIONS_ID_TOKEN_REQUEST_URL") ||
		!strings.Contains(err.Error(), "ACTIONS_ID_TOKEN_REQUEST_TOKEN") {
		t.Fatalf("error does not name both env vars: %v", err)
	}
	if !strings.Contains(err.Error(), "id-token: write") {
		t.Fatalf("error does not mention the likely missing job permission: %v", err)
	}
}

// TestFetchGitHubOIDCToken_SuccessNoAudience proves the no-audience case: the fake server
// asserts the request carries no "audience=" query param at all, and the JWT decoded from
// {"value": "<jwt>"} is returned unchanged.
func TestFetchGitHubOIDCToken_SuccessNoAudience(t *testing.T) {
	const wantJWT = "fake.jwt.token"
	const wantBearer = "fake-request-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "bearer "+wantBearer {
			t.Errorf("Authorization header = %q, want %q", got, "bearer "+wantBearer)
		}
		if strings.Contains(r.URL.RawQuery, "audience=") {
			t.Errorf("request carries audience= with no audience requested: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"value":%q}`, wantJWT)
	}))
	defer server.Close()

	// Mirror a real GitHub runner: ACTIONS_ID_TOKEN_REQUEST_URL already carries its own query
	// string.
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", server.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", wantBearer)

	got, err := fetchGitHubOIDCToken(context.Background(), "")
	if err != nil {
		t.Fatalf("fetchGitHubOIDCToken: %v", err)
	}
	if got != wantJWT {
		t.Fatalf("token = %q, want %q", got, wantJWT)
	}
}

// TestFetchGitHubOIDCToken_AudienceAppendedWithAmpersand proves audience is appended with a
// literal "&", not "?" -- ACTIONS_ID_TOKEN_REQUEST_URL already has its own query string on a
// real GitHub runner, so a "?"-prefixed guess would silently corrupt the request URL.
func TestFetchGitHubOIDCToken_AudienceAppendedWithAmpersand(t *testing.T) {
	const wantAudience = "some-aud"
	var gotRequestURI string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequestURI = r.RequestURI
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"value":"jwt-with-audience"}`)
	}))
	defer server.Close()

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", server.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "tok")

	if _, err := fetchGitHubOIDCToken(context.Background(), wantAudience); err != nil {
		t.Fatalf("fetchGitHubOIDCToken: %v", err)
	}

	if !strings.Contains(gotRequestURI, "&audience="+wantAudience) {
		t.Fatalf("request URI %q does not contain the literal '&audience=%s'", gotRequestURI, wantAudience)
	}
}

// TestFetchGitHubOIDCToken_NonOKStatus proves a non-200 response is a named, specific error.
func TestFetchGitHubOIDCToken_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", server.URL)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "tok")

	_, err := fetchGitHubOIDCToken(context.Background(), "")
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error does not name the status: %v", err)
	}
}

// TestFetchGitHubOIDCToken_EmptyValue proves an empty "value" field is a hard error, never a
// silently-empty JWT flowing into the token exchange.
func TestFetchGitHubOIDCToken_EmptyValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"value":""}`)
	}))
	defer server.Close()

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", server.URL)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "tok")

	if _, err := fetchGitHubOIDCToken(context.Background(), ""); err == nil {
		t.Fatal("expected an error for an empty value field")
	}
}

// TestGithubOIDCTokenIssuer_RefetchesOnEveryCall is the property this plan's objective and
// CONTEXT.md's UPST-renewal decision depend on: GetToken() must call fetchGitHubOIDCToken
// fresh on every invocation, not return a value cached at construction. Proven by swapping the
// env vars between two GetToken() calls on the SAME issuer instance and observing both fetches
// reflect the current env.
func TestGithubOIDCTokenIssuer_RefetchesOnEveryCall(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"value":"jwt-%d"}`, callCount)
	}))
	defer server.Close()

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", server.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "tok-1")

	issuer := &githubOIDCTokenIssuer{audience: "x"}

	first, err := issuer.GetToken()
	if err != nil {
		t.Fatalf("first GetToken: %v", err)
	}
	if first != "jwt-1" {
		t.Fatalf("first token = %q, want jwt-1", first)
	}

	// Swap the env vars the SAME issuer instance reads from; if GetToken cached the first
	// fetch, this second call would still return "jwt-1" instead of reflecting the new state.
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "tok-2")

	second, err := issuer.GetToken()
	if err != nil {
		t.Fatalf("second GetToken: %v", err)
	}
	if second != "jwt-2" {
		t.Fatalf("second token = %q, want jwt-2 (issuer is caching instead of re-fetching)", second)
	}
	if callCount != 2 {
		t.Fatalf("server received %d requests, want 2", callCount)
	}
}
