package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/znd4/terraform-provider-desec/internal/desec"
)

var (
	_ resource.ResourceWithConfigure   = (*tokenPolicyResource)(nil)
	_ resource.ResourceWithImportState = (*tokenPolicyResource)(nil)
)

type tokenPolicyResource struct{ client *desec.Client }

type tokenPolicyModel struct {
	ID        types.String `tfsdk:"id"`
	TokenID   types.String `tfsdk:"token_id"`
	Domain    types.String `tfsdk:"domain"`
	Subname   types.String `tfsdk:"subname"`
	Type      types.String `tfsdk:"type"`
	PermWrite types.Bool   `tfsdk:"perm_write"`
}

// NewTokenPolicyResource is the desec_token_policy resource.
func NewTokenPolicyResource() resource.Resource { return &tokenPolicyResource{} }

func (r *tokenPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_token_policy"
}

const policyOrderHelp = "deSEC requires the default policy (domain, subname and type all null) " +
	"before any other policy of the token, and deletes it last. " +
	"Add depends_on = [<default policy>] to each other policy of the token."

func (r *tokenPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	nullable := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Description: desc + " Null matches all values.", Optional: true}
	}
	resp.Schema = schema.Schema{
		Description: "An RRset policy of a deSEC token. A token with a policy can write only the RRsets " +
			"that a policy with perm_write allows. " + policyOrderHelp,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Policy ID.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"token_id": schema.StringAttribute{
				Description:   "ID of the token (desec_token.id).",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"domain":  nullable("Domain name."),
			"subname": nullable("Subname. Empty string is the zone apex."),
			"type":    nullable("Record type, for example TXT."),
			"perm_write": schema.BoolAttribute{
				Description: "The token can write the matching RRsets.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
		},
	}
}

func (r *tokenPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (m *tokenPolicyModel) policy() desec.TokenPolicy {
	return desec.TokenPolicy{
		Domain:    stringPtr(m.Domain),
		Subname:   stringPtr(m.Subname),
		Type:      stringPtr(m.Type),
		PermWrite: m.PermWrite.ValueBool(),
	}
}

func nullableString(s *string) types.String {
	if s == nil {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

func (m *tokenPolicyModel) fill(p *desec.TokenPolicy) {
	m.ID = types.StringValue(p.ID)
	m.Domain = nullableString(p.Domain)
	m.Subname = nullableString(p.Subname)
	m.Type = nullableString(p.Type)
	m.PermWrite = types.BoolValue(p.PermWrite)
}

func policyError(summary string, err error) (string, string) {
	var apiErr *desec.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 400 && strings.Contains(strings.ToLower(apiErr.Body), "default") {
		return summary, err.Error() + "\n\n" + policyOrderHelp
	}
	return summary, err.Error()
}

func (r *tokenPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan tokenPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.CreateTokenPolicy(ctx, plan.TokenID.ValueString(), plan.policy())
	if err != nil {
		resp.Diagnostics.AddError(policyError("Cannot create token policy", err))
		return
	}
	plan.fill(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *tokenPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state tokenPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.GetTokenPolicy(ctx, state.TokenID.ValueString(), state.ID.ValueString())
	if desec.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read token policy", err.Error())
		return
	}
	state.fill(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *tokenPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state tokenPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.UpdateTokenPolicy(ctx, state.TokenID.ValueString(), state.ID.ValueString(), plan.policy())
	if err != nil {
		resp.Diagnostics.AddError(policyError("Cannot update token policy", err))
		return
	}
	plan.fill(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *tokenPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state tokenPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteTokenPolicy(ctx, state.TokenID.ValueString(), state.ID.ValueString())
	if err != nil && !desec.IsNotFound(err) {
		resp.Diagnostics.AddError(policyError("Cannot delete token policy", err))
	}
}

func (r *tokenPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	tokenID, policyID, ok := strings.Cut(req.ID, "/")
	if !ok || tokenID == "" || policyID == "" || strings.Contains(policyID, "/") {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("want <token_id>/<policy_id>, got %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("token_id"), tokenID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), policyID)...)
}

func errorFromDiags(d diag.Diagnostics) error {
	var msgs []string
	for _, e := range d.Errors() {
		msgs = append(msgs, e.Summary()+": "+e.Detail())
	}
	return errors.New(strings.Join(msgs, "; "))
}
