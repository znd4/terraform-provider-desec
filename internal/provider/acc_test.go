package provider

// Acceptance tests. They make and delete real objects in a deSEC account.
//
//   TF_ACC=1                     run them (terraform-plugin-testing skips them otherwise)
//   DESEC_TOKEN=...              a token with perm_manage_tokens, perm_create_domain, perm_delete_domain
//   DESEC_ACC_DOMAIN_SUFFIX=...  optional: tests make tfacc-<random>.<suffix>; without it the domain test skips

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func accPreCheck(t *testing.T) {
	if os.Getenv("DESEC_TOKEN") == "" {
		t.Fatal("DESEC_TOKEN must be set for acceptance tests")
	}
}

func TestAccToken(t *testing.T) {
	requireTofu(t)
	name := "tfacc-" + acctest.RandString(8)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "desec_token" "t" {
  name            = %q
  allowed_subnets = ["192.0.2.0/24"]
}
resource "desec_token_policy" "default" {
  token_id = desec_token.t.id
}
resource "desec_token_policy" "txt" {
  token_id   = desec_token.t.id
  domain     = "example.invalid"
  subname    = "_acme-challenge"
  type       = "TXT"
  perm_write = true
  depends_on = [desec_token_policy.default]
}
`, name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("desec_token.t", "name", name),
					resource.TestCheckResourceAttr("desec_token.t", "allowed_subnets.0", "192.0.2.0/24"),
					resource.TestCheckResourceAttrSet("desec_token.t", "token"),
					resource.TestCheckNoResourceAttr("desec_token_policy.default", "domain"),
					resource.TestCheckResourceAttr("desec_token_policy.txt", "perm_write", "true"),
				),
			},
			{
				RefreshState: true,
				Check:        resource.TestCheckResourceAttrSet("desec_token.t", "token"),
			},
			{
				ResourceName:      "desec_token_policy.txt",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources["desec_token_policy.txt"]
					return rs.Primary.Attributes["token_id"] + "/" + rs.Primary.ID, nil
				},
			},
		},
	})
}

func TestAccDomainAndRRSet(t *testing.T) {
	requireTofu(t)
	suffix := os.Getenv("DESEC_ACC_DOMAIN_SUFFIX")
	if suffix == "" && os.Getenv("TF_ACC") != "" {
		t.Skip("DESEC_ACC_DOMAIN_SUFFIX is not set")
	}
	domain := "tfacc-" + strings.ToLower(acctest.RandString(8)) + "." + suffix
	config := fmt.Sprintf(`
resource "desec_domain" "d" {
  name = %q
}
resource "desec_rrset" "txt" {
  domain  = desec_domain.d.name
  subname = "_acme-challenge"
  type    = "TXT"
  ttl     = desec_domain.d.minimum_ttl
  records = ["\"hello\""]
}
resource "desec_rrset" "apex_a" {
  domain  = desec_domain.d.name
  type    = "AAAA"
  ttl     = desec_domain.d.minimum_ttl
  records = ["2001:db8:0::1"]
}
`, domain)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("desec_rrset.txt", "name", "_acme-challenge."+domain+"."),
					// deSEC stores 2001:db8::1. The configured form stays.
					resource.TestCheckTypeSetElemAttr("desec_rrset.apex_a", "records.*", "2001:db8:0::1"),
				),
			},
			{Config: config, PlanOnly: true},
			{
				ResourceName:      "desec_rrset.txt",
				ImportState:       true,
				ImportStateId:     domain + "/_acme-challenge/TXT",
				ImportStateVerify: true,
			},
		},
	})
}
