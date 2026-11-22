package desec_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/znd4/terraform-provider-desec/internal/desec"
	"github.com/znd4/terraform-provider-desec/internal/desec/fake"
)

func ptr[T any](v T) *T { return &v }

func TestAuthorizationHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"name":"example.com"}`)
	}))
	defer srv.Close()

	c := desec.New(srv.URL, "abc")
	if _, err := c.GetDomain(context.Background(), "example.com"); err != nil {
		t.Fatal(err)
	}
	if got != "Token abc" {
		t.Fatalf("Authorization = %q, want %q", got, "Token abc")
	}
}

func TestRetryOn429(t *testing.T) {
	f := fake.New()
	defer f.Close()
	c := f.Client()
	f.Throttle.Store(3)

	if _, err := c.CreateDomain(context.Background(), "example.com"); err != nil {
		t.Fatalf("CreateDomain after throttling: %v", err)
	}
	if n := len(f.Requests); n != 4 {
		t.Fatalf("requests = %d, want 4 (3 throttled + 1)", n)
	}
}

func TestRetryGivesUp(t *testing.T) {
	f := fake.New()
	defer f.Close()
	c := f.Client()
	c.MaxRetries = 2
	f.Throttle.Store(10)

	_, err := c.CreateDomain(context.Background(), "example.com")
	var apiErr *desec.APIError
	if !strings.Contains(err.Error(), "429") || !asAPIError(err, &apiErr) {
		t.Fatalf("err = %v, want a 429 APIError", err)
	}
}

func asAPIError(err error, target **desec.APIError) bool {
	e, ok := err.(*desec.APIError)
	if ok {
		*target = e
	}
	return ok
}

func TestNotFound(t *testing.T) {
	f := fake.New()
	defer f.Close()
	_, err := f.Client().GetDomain(context.Background(), "missing.example")
	if !desec.IsNotFound(err) {
		t.Fatalf("IsNotFound(%v) = false", err)
	}
}

func TestApexRRSetUsesAt(t *testing.T) {
	f := fake.New()
	defer f.Close()
	c := f.Client()
	ctx := context.Background()
	if _, err := c.CreateDomain(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateRRSet(ctx, "example.com", desec.RRSet{Type: "TXT", TTL: 3600, Records: []string{`"x"`}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetRRSet(ctx, "example.com", "", "TXT"); err != nil {
		t.Fatal(err)
	}
	want := "GET /api/v1/domains/example.com/rrsets/@/TXT/"
	if !slices.Contains(f.Requests, want) {
		t.Fatalf("requests %v do not contain %q", f.Requests, want)
	}
}

func TestTokenPolicySendsExplicitNulls(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"p1","domain":null,"subname":null,"type":null,"perm_write":false}`)
	}))
	defer srv.Close()

	_, err := desec.New(srv.URL, "x").CreateTokenPolicy(context.Background(), "t1", desec.TokenPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"domain", "subname", "type", "perm_write"} {
		v, ok := body[k]
		if !ok {
			t.Errorf("body has no %q key; deSEC needs explicit nulls", k)
		}
		if k != "perm_write" && v != nil {
			t.Errorf("%s = %v, want null", k, v)
		}
	}
}

func TestEmptySubnameIsNotNull(t *testing.T) {
	b, err := json.Marshal(desec.TokenPolicy{Domain: ptr("example.com"), Subname: ptr(""), Type: nil})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"subname":""`) || !strings.Contains(string(b), `"type":null`) {
		t.Fatalf("marshal = %s", b)
	}
}

func TestPolicyOrder(t *testing.T) {
	f := fake.New()
	defer f.Close()
	c := f.Client()
	ctx := context.Background()

	tok, err := c.CreateToken(ctx, desec.TokenRequest{Name: ptr("t")})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value == "" {
		t.Fatal("CreateToken returned no secret")
	}
	specific := desec.TokenPolicy{Domain: ptr("example.com"), Subname: ptr("_acme-challenge"), Type: ptr("TXT"), PermWrite: true}
	if _, err := c.CreateTokenPolicy(ctx, tok.ID, specific); err == nil {
		t.Fatal("specific policy before the default policy: want an error")
	}
	def, err := c.CreateTokenPolicy(ctx, tok.ID, desec.TokenPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTokenPolicy(ctx, tok.ID, specific); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteTokenPolicy(ctx, tok.ID, def.ID); err == nil {
		t.Fatal("delete default policy while others exist: want an error")
	}

	got, err := c.GetToken(ctx, tok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "" {
		t.Fatal("GetToken must not return the secret")
	}
}
