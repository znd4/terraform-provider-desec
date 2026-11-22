// Package provider is the deSEC provider for OpenTofu and Terraform.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/znd4/terraform-provider-desec/internal/desec"
)

var _ provider.Provider = (*desecProvider)(nil)

type desecProvider struct {
	version string
}

type providerModel struct {
	Token  types.String `tfsdk:"token"`
	APIURL types.String `tfsdk:"api_url"`
}

// New returns a provider factory.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &desecProvider{version: version} }
}

func (p *desecProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "desec"
	resp.Version = p.version
}

func (p *desecProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage deSEC (https://desec.io) domains, RRsets, tokens and token policies.",
		Attributes: map[string]schema.Attribute{
			"token": schema.StringAttribute{
				Description: "deSEC API token. Defaults to the DESEC_TOKEN environment variable. " +
					"To manage tokens and policies, the token needs perm_manage_tokens.",
				Optional:  true,
				Sensitive: true,
			},
			"api_url": schema.StringAttribute{
				Description: "API base URL. Defaults to DESEC_API_URL, then " + desec.DefaultBaseURL + ".",
				Optional:    true,
			},
		},
	}
}

func (p *desecProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.Token.IsUnknown() || cfg.APIURL.IsUnknown() {
		// Values come from other resources. Wait for the apply.
		return
	}

	token := os.Getenv("DESEC_TOKEN")
	if !cfg.Token.IsNull() {
		token = cfg.Token.ValueString()
	}
	apiURL := os.Getenv("DESEC_API_URL")
	if !cfg.APIURL.IsNull() {
		apiURL = cfg.APIURL.ValueString()
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing deSEC token",
			"Set the provider attribute token or the environment variable DESEC_TOKEN.")
		return
	}

	c := desec.New(apiURL, token)
	c.UserAgent = "terraform-provider-desec/" + p.version
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *desecProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewDomainResource,
		NewRRSetResource,
		NewTokenResource,
		NewTokenPolicyResource,
	}
}

func (p *desecProvider) DataSources(context.Context) []func() datasource.DataSource {
	return nil
}

// clientFrom reads the client from ProviderData. It returns nil before the
// provider is configured.
func clientFrom(data any, diags interface {
	AddError(string, string)
}) *desec.Client {
	if data == nil {
		return nil
	}
	c, ok := data.(*desec.Client)
	if !ok {
		diags.AddError("Unexpected provider data", "The provider data is not a *desec.Client. This is a bug in the provider.")
		return nil
	}
	return c
}

// stringPtr maps a null string to nil.
func stringPtr(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

func boolPtr(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	b := v.ValueBool()
	return &b
}
