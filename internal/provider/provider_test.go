package provider

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

var testFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"desec": providerserver.NewProtocol6WithError(New("test")()),
}

// requireTofu points terraform-plugin-testing at a local tofu or terraform
// binary, so the tests never download one. The tests skip if there is none.
func requireTofu(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC_TERRAFORM_PATH") != "" {
		return
	}
	for _, bin := range []string{"tofu", "terraform"} {
		if p, err := exec.LookPath(bin); err == nil {
			t.Setenv("TF_ACC_TERRAFORM_PATH", p)
			if bin == "tofu" {
				t.Setenv("TF_ACC_PROVIDER_HOST", "registry.opentofu.org")
			}
			return
		}
	}
	t.Skip("no tofu or terraform binary on PATH")
}

func providerBlock(apiURL, token string) string {
	return fmt.Sprintf(`
provider "desec" {
  api_url = %q
  token   = %q
}
`, apiURL, token)
}
