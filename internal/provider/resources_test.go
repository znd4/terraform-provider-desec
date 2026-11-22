package provider

// These tests run the provider through tofu against the in-memory fake API.
// They do not need TF_ACC or network access. acc_test.go has the tests
// against the real deSEC API.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/znd4/terraform-provider-desec/internal/desec/fake"
)

func TestTokenAndPolicies(t *testing.T) {
	requireTofu(t)
	f := fake.New()
	defer f.Close()

	config := providerBlock(f.BaseURL(), f.Token) + `
resource "desec_token" "acme" {
  name = "hl01-acme"
}

resource "desec_token_policy" "default" {
  token_id = desec_token.acme.id
}

resource "desec_token_policy" "challenge" {
  token_id   = desec_token.acme.id
  domain     = "acme.example.com"
  subname    = "_acme-challenge"
  type       = "TXT"
  perm_write = true
  depends_on = [desec_token_policy.default]
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("desec_token.acme", "name", "hl01-acme"),
					resource.TestCheckResourceAttr("desec_token.acme", "perm_manage_tokens", "false"),
					resource.TestCheckResourceAttrWith("desec_token.acme", "token", func(v string) error {
						if !strings.HasPrefix(v, "secret-") {
							return fmt.Errorf("token = %q, want the secret from create", v)
						}
						return nil
					}),
					resource.TestCheckNoResourceAttr("desec_token_policy.default", "domain"),
					resource.TestCheckNoResourceAttr("desec_token_policy.default", "subname"),
					resource.TestCheckNoResourceAttr("desec_token_policy.default", "type"),
					resource.TestCheckResourceAttr("desec_token_policy.default", "perm_write", "false"),
					resource.TestCheckResourceAttr("desec_token_policy.challenge", "subname", "_acme-challenge"),
					resource.TestCheckResourceAttr("desec_token_policy.challenge", "perm_write", "true"),
				),
			},
			{
				// A refresh must keep the secret (Valodim/terraform-provider-desec#23).
				RefreshState: true,
				Check: resource.TestCheckResourceAttrWith("desec_token.acme", "token", func(v string) error {
					if v == "" {
						return fmt.Errorf("token secret lost after refresh")
					}
					return nil
				}),
			},
			{
				// An in-place rename keeps the ID and the secret.
				Config: strings.Replace(config, `name = "hl01-acme"`, `name = "hl01-acme-renamed"`, 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("desec_token.acme", "name", "hl01-acme-renamed"),
					resource.TestCheckResourceAttrPair("desec_token.acme", "id", "desec_token_policy.default", "token_id"),
					resource.TestCheckResourceAttrWith("desec_token.acme", "token", func(v string) error {
						if !strings.HasPrefix(v, "secret-") {
							return fmt.Errorf("token secret changed or lost on update: %q", v)
						}
						return nil
					}),
				),
			},
			{
				ResourceName:      "desec_token_policy.challenge",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources["desec_token_policy.challenge"]
					return rs.Primary.Attributes["token_id"] + "/" + rs.Primary.ID, nil
				},
			},
			{
				ResourceName:            "desec_token.acme",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"token"},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if ids := f.TokenIDs(); len(ids) != 0 {
				return fmt.Errorf("tokens left after destroy: %v", ids)
			}
			return nil
		},
	})
}

// The default policy can be the apex of one domain: subname "" is not null.
func TestPolicyEmptySubnameIsNotNull(t *testing.T) {
	requireTofu(t)
	f := fake.New()
	defer f.Close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{{
			Config: providerBlock(f.BaseURL(), f.Token) + `
resource "desec_token" "t" {}
resource "desec_token_policy" "default" { token_id = desec_token.t.id }
resource "desec_token_policy" "apex" {
  token_id   = desec_token.t.id
  domain     = "example.com"
  subname    = ""
  perm_write = true
  depends_on = [desec_token_policy.default]
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("desec_token_policy.apex", "subname", ""),
				resource.TestCheckNoResourceAttr("desec_token_policy.apex", "type"),
				func(*terraform.State) error {
					ids := f.TokenIDs()
					for _, p := range f.Policies(ids[0]) {
						if p.Domain != nil && (p.Subname == nil || *p.Subname != "") {
							return fmt.Errorf("server policy subname = %v, want empty string", p.Subname)
						}
					}
					return nil
				},
			),
		}},
	})
}

func TestDomainAndRRSet(t *testing.T) {
	requireTofu(t)
	f := fake.New()
	defer f.Close()
	// Like the real API: the server stores names in lower case.
	f.Canonicalize = func(typ, v string) string {
		if typ == "CNAME" {
			return strings.ToLower(v)
		}
		return v
	}

	config := providerBlock(f.BaseURL(), f.Token) + `
resource "desec_domain" "acme" {
  name = "acme.example.com"
}

resource "desec_rrset" "challenge" {
  domain  = desec_domain.acme.name
  subname = "_acme-challenge"
  type    = "TXT"
  ttl     = 3600
  records = ["\"a\"", "\"b\""]
}

resource "desec_rrset" "apex_txt" {
  domain  = desec_domain.acme.name
  type    = "TXT"
  ttl     = 3600
  records = ["\"apex\""]
}

resource "desec_rrset" "cname" {
  domain  = desec_domain.acme.name
  subname = "www"
  type    = "CNAME"
  ttl     = 3600
  records = ["Target.Example.NET."]
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("desec_domain.acme", "id", "acme.example.com"),
					resource.TestCheckResourceAttr("desec_domain.acme", "minimum_ttl", "3600"),
					resource.TestCheckResourceAttr("desec_domain.acme", "ds.#", "2"),
					resource.TestCheckResourceAttr("desec_rrset.challenge", "id", "acme.example.com/_acme-challenge/TXT"),
					resource.TestCheckResourceAttr("desec_rrset.challenge", "name", "_acme-challenge.acme.example.com."),
					resource.TestCheckResourceAttr("desec_rrset.challenge", "records.#", "2"),
					resource.TestCheckResourceAttr("desec_rrset.apex_txt", "id", "acme.example.com/@/TXT"),
					resource.TestCheckResourceAttr("desec_rrset.apex_txt", "subname", ""),
					// The configured (non-canonical) value stays in state.
					resource.TestCheckTypeSetElemAttr("desec_rrset.cname", "records.*", "Target.Example.NET."),
				),
			},
			{
				// No diff after a refresh, although the server stores the CNAME in lower case.
				Config:   config,
				PlanOnly: true,
			},
			{
				Config: strings.Replace(config, `ttl     = 3600
  records = ["\"a\"", "\"b\""]`, `ttl     = 7200
  records = ["\"c\""]`, 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("desec_rrset.challenge", "ttl", "7200"),
					resource.TestCheckResourceAttr("desec_rrset.challenge", "records.#", "1"),
					resource.TestCheckResourceAttr("desec_rrset.challenge", "id", "acme.example.com/_acme-challenge/TXT"),
				),
			},
			{
				ResourceName:      "desec_rrset.apex_txt",
				ImportState:       true,
				ImportStateId:     "acme.example.com/@/TXT",
				ImportStateVerify: true,
			},
			{
				ResourceName:      "desec_domain.acme",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// A change outside of OpenTofu shows as drift.
				PreConfig: func() {
					f.SetRecords("acme.example.com", "", "TXT", []string{`"changed"`})
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestParseRRSetID(t *testing.T) {
	for _, tc := range []struct {
		in, domain, subname, typ string
		ok                       bool
	}{
		{"example.com/www/A", "example.com", "www", "A", true},
		{"example.com/@/TXT", "example.com", "", "TXT", true},
		{"example.com//TXT", "example.com", "", "TXT", true},
		{"example.com/a.b/TXT", "example.com", "a.b", "TXT", true},
		{"example.com/TXT", "", "", "", false},
		{"example.com/www/A/x", "", "", "", false},
	} {
		d, s, ty, err := parseRRSetID(tc.in)
		if (err == nil) != tc.ok || d != tc.domain || s != tc.subname || ty != tc.typ {
			t.Errorf("parseRRSetID(%q) = %q, %q, %q, %v", tc.in, d, s, ty, err)
		}
	}
	if got := rrsetID("example.com", "", "TXT"); got != "example.com/@/TXT" {
		t.Errorf("rrsetID apex = %q", got)
	}
}
