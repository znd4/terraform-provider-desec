package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/znd4/terraform-provider-desec/internal/desec"
)

var (
	_ resource.ResourceWithConfigure   = (*domainResource)(nil)
	_ resource.ResourceWithImportState = (*domainResource)(nil)
)

type domainResource struct{ client *desec.Client }

type domainModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	MinimumTTL types.Int64  `tfsdk:"minimum_ttl"`
	Created    types.String `tfsdk:"created"`
	DS         types.List   `tfsdk:"ds"`
}

// NewDomainResource is the desec_domain resource.
func NewDomainResource() resource.Resource { return &domainResource{} }

func (r *domainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain"
}

func (r *domainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A deSEC domain (DNS zone). deSEC adds the NS records of its name servers. " +
			"Deleting the resource deletes the zone and all its records.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The domain name.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "The domain name, for example acme.example.com. Changing it replaces the domain.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"minimum_ttl": schema.Int64Attribute{
				Description:   "The smallest TTL that RRsets in this domain can have. The server sets it.",
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"created": schema.StringAttribute{
				Description:   "Creation time.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ds": schema.ListAttribute{
				Description:   "DS records for the parent zone (DNSSEC), in presentation format without the owner name.",
				ElementType:   types.StringType,
				Computed:      true,
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *domainResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (m *domainModel) fill(d *desec.Domain) {
	m.ID = types.StringValue(d.Name)
	m.Name = types.StringValue(d.Name)
	m.MinimumTTL = types.Int64Value(d.MinimumTTL)
	m.Created = types.StringValue(d.Created)
	var ds []attr.Value
	for _, k := range d.Keys {
		for _, v := range k.DS {
			ds = append(ds, types.StringValue(v))
		}
	}
	m.DS = types.ListValueMust(types.StringType, ds)
	if ds == nil {
		m.DS = types.ListValueMust(types.StringType, []attr.Value{})
	}
}

func (r *domainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan domainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.client.CreateDomain(ctx, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot create domain", err.Error())
		return
	}
	// The create response may omit keys. Read the domain again.
	if full, err := r.client.GetDomain(ctx, d.Name); err == nil {
		d = full
	}
	plan.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *domainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state domainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.client.GetDomain(ctx, state.ID.ValueString())
	if desec.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read domain", err.Error())
		return
	}
	state.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: name requires replacement, and the rest is computed.
func (r *domainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan domainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *domainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state domainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteDomain(ctx, state.ID.ValueString()); err != nil && !desec.IsNotFound(err) {
		resp.Diagnostics.AddError("Cannot delete domain", err.Error())
	}
}

func (r *domainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
