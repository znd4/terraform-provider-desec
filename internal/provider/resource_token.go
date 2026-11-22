package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/znd4/terraform-provider-desec/internal/desec"
)

var (
	_ resource.ResourceWithConfigure   = (*tokenResource)(nil)
	_ resource.ResourceWithImportState = (*tokenResource)(nil)
)

type tokenResource struct{ client *desec.Client }

type tokenModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	PermCreateDomain types.Bool   `tfsdk:"perm_create_domain"`
	PermDeleteDomain types.Bool   `tfsdk:"perm_delete_domain"`
	PermManageTokens types.Bool   `tfsdk:"perm_manage_tokens"`
	AutoPolicy       types.Bool   `tfsdk:"auto_policy"`
	AllowedSubnets   types.List   `tfsdk:"allowed_subnets"`
	Created          types.String `tfsdk:"created"`
	Owner            types.String `tfsdk:"owner"`
	IsValid          types.Bool   `tfsdk:"is_valid"`
	Token            types.String `tfsdk:"token"`
}

// NewTokenResource is the desec_token resource.
func NewTokenResource() resource.Resource { return &tokenResource{} }

func (r *tokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_token"
}

func (r *tokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	flag := func(desc string) schema.BoolAttribute {
		return schema.BoolAttribute{Description: desc, Optional: true, Computed: true, Default: booldefault.StaticBool(false)}
	}
	resp.Schema = schema.Schema{
		Description: "A deSEC API token. The provider token needs perm_manage_tokens. " +
			"deSEC returns the secret only when it creates the token, so the secret is in the state. " +
			"Encrypt the state.",
		Attributes: map[string]schema.Attribute{
			"id":                 schema.StringAttribute{Description: "Token ID (not the secret).", Computed: true, PlanModifiers: keep},
			"name":               schema.StringAttribute{Description: "Token name.", Optional: true, Computed: true, Default: stringdefault.StaticString("")},
			"perm_create_domain": flag("The token can create domains."),
			"perm_delete_domain": flag("The token can delete domains."),
			"perm_manage_tokens": flag("The token can manage tokens and policies."),
			"auto_policy": flag("When this token creates a domain, deSEC adds a write policy for it. " +
				"deSEC then adds a default policy itself, so do not also manage a default policy for this token."),
			"allowed_subnets": schema.ListAttribute{
				Description:   "IP ranges that can use the token. The server default is all addresses (0.0.0.0/0 and ::/0).",
				ElementType:   types.StringType,
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"created":  schema.StringAttribute{Description: "Creation time.", Computed: true, PlanModifiers: keep},
			"owner":    schema.StringAttribute{Description: "Email address of the account.", Computed: true, PlanModifiers: keep},
			"is_valid": schema.BoolAttribute{Description: "False after the token expires.", Computed: true},
			"token": schema.StringAttribute{
				Description:   "The secret. Set only when the provider creates the token. Null after import.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: keep,
			},
		},
	}
}

func (r *tokenResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (m *tokenModel) request(ctx context.Context) (desec.TokenRequest, error) {
	req := desec.TokenRequest{
		Name:             stringPtr(m.Name),
		PermCreateDomain: boolPtr(m.PermCreateDomain),
		PermDeleteDomain: boolPtr(m.PermDeleteDomain),
		PermManageTokens: boolPtr(m.PermManageTokens),
		AutoPolicy:       boolPtr(m.AutoPolicy),
	}
	if !m.AllowedSubnets.IsNull() && !m.AllowedSubnets.IsUnknown() {
		var subnets []string
		if d := m.AllowedSubnets.ElementsAs(ctx, &subnets, false); d.HasError() {
			return req, errorFromDiags(d)
		}
		req.AllowedSubnets = subnets
	}
	return req, nil
}

// fill sets every attribute except token from the server.
func (m *tokenModel) fill(ctx context.Context, t *desec.Token) {
	m.ID = types.StringValue(t.ID)
	m.Name = types.StringValue(t.Name)
	m.PermCreateDomain = types.BoolValue(t.PermCreateDomain)
	m.PermDeleteDomain = types.BoolValue(t.PermDeleteDomain)
	m.PermManageTokens = types.BoolValue(t.PermManageTokens)
	m.AutoPolicy = types.BoolValue(t.AutoPolicy)
	subnets := t.AllowedSubnets
	if subnets == nil {
		subnets = []string{}
	}
	m.AllowedSubnets, _ = types.ListValueFrom(ctx, types.StringType, subnets)
	m.Created = types.StringValue(t.Created)
	m.Owner = types.StringValue(t.Owner)
	m.IsValid = types.BoolValue(t.IsValid)
}

func (r *tokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan tokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := plan.request(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Invalid configuration", err.Error())
		return
	}
	t, err := r.client.CreateToken(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Cannot create token", err.Error())
		return
	}
	plan.fill(ctx, t)
	plan.Token = types.StringValue(t.Value)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *tokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state tokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.client.GetToken(ctx, state.ID.ValueString())
	if desec.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read token", err.Error())
		return
	}
	state.fill(ctx, t) // state.Token stays: the API never returns it again.
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *tokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state tokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := plan.request(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Invalid configuration", err.Error())
		return
	}
	t, err := r.client.UpdateToken(ctx, state.ID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Cannot update token", err.Error())
		return
	}
	plan.fill(ctx, t)
	plan.Token = state.Token
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *tokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state tokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteToken(ctx, state.ID.ValueString()); err != nil && !desec.IsNotFound(err) {
		resp.Diagnostics.AddError("Cannot delete token", err.Error())
	}
}

func (r *tokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	resp.Diagnostics.AddWarning("Token secret not imported",
		"deSEC returns the secret only at creation, so the token attribute stays null.")
}
