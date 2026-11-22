package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/znd4/terraform-provider-desec/internal/desec"
)

var (
	_ resource.ResourceWithConfigure   = (*rrsetResource)(nil)
	_ resource.ResourceWithImportState = (*rrsetResource)(nil)
)

// privateServerRecords is the private state key for the records that the
// server returned on the last write.
const privateServerRecords = "server_records"

type rrsetResource struct{ client *desec.Client }

type rrsetModel struct {
	ID      types.String `tfsdk:"id"`
	Domain  types.String `tfsdk:"domain"`
	Subname types.String `tfsdk:"subname"`
	Type    types.String `tfsdk:"type"`
	TTL     types.Int64  `tfsdk:"ttl"`
	Records types.Set    `tfsdk:"records"`
	Name    types.String `tfsdk:"name"`
}

// NewRRSetResource is the desec_rrset resource.
func NewRRSetResource() resource.Resource { return &rrsetResource{} }

func (r *rrsetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rrset"
}

func (r *rrsetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A deSEC RRset: all records of one type at one name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "<domain>/<subname>/<type>. The subname is @ at the zone apex.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"domain": schema.StringAttribute{
				Description:   "The deSEC domain.",
				Required:      true,
				PlanModifiers: replace,
			},
			"subname": schema.StringAttribute{
				Description:   "The name below the domain. Empty (the default) is the zone apex.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString(""),
				PlanModifiers: replace,
				Validators:    []validator.String{stringvalidator.NoneOf("@")},
			},
			"type": schema.StringAttribute{
				Description:   "The record type in upper case, for example TXT.",
				Required:      true,
				PlanModifiers: replace,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					upperCase{},
				},
			},
			"ttl": schema.Int64Attribute{
				Description: "TTL in seconds. It must be at least the minimum_ttl of the domain and at most 86400.",
				Required:    true,
				Validators:  []validator.Int64{int64validator.Between(1, 86400)},
			},
			"records": schema.SetAttribute{
				Description: "Record values in presentation format. TXT values need double quotes. " +
					"Names in CNAME, MX, NS and SRV values need a final dot.",
				ElementType: types.StringType,
				Required:    true,
				Validators:  []validator.Set{setvalidator.SizeAtLeast(1)},
			},
			"name": schema.StringAttribute{
				Description:   "The full name with a final dot.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *rrsetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func rrsetID(domain, subname, typ string) string {
	if subname == "" {
		subname = "@"
	}
	return domain + "/" + subname + "/" + typ
}

func parseRRSetID(id string) (domain, subname, typ string, err error) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("want <domain>/<subname>/<type> (subname @ for the apex), got %q", id)
	}
	subname = parts[1]
	if subname == "@" {
		subname = ""
	}
	return parts[0], subname, parts[2], nil
}

func sortedCopy(v []string) []string {
	out := slices.Clone(v)
	slices.Sort(out)
	return out
}

// privateGetter and privateSetter cover the private state types of the
// different responses.
type privateGetter interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
}
type privateSetter interface {
	SetKey(context.Context, string, []byte) diag.Diagnostics
}

func saveServerRecords(ctx context.Context, p privateSetter, records []string) diag.Diagnostics {
	b, _ := json.Marshal(sortedCopy(records))
	return p.SetKey(ctx, privateServerRecords, b)
}

func loadServerRecords(ctx context.Context, p privateGetter) ([]string, bool, diag.Diagnostics) {
	b, diags := p.GetKey(ctx, privateServerRecords)
	if diags.HasError() || len(b) == 0 {
		return nil, false, diags
	}
	var out []string
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, false, diags
	}
	return out, true, diags
}

func (m *rrsetModel) setFromServer(rr *desec.RRSet) {
	m.ID = types.StringValue(rrsetID(m.Domain.ValueString(), rr.Subname, rr.Type))
	m.Subname = types.StringValue(rr.Subname)
	m.Type = types.StringValue(rr.Type)
	m.TTL = types.Int64Value(rr.TTL)
	m.Name = types.StringValue(rr.Name)
}

func recordsOf(ctx context.Context, set types.Set) ([]string, diag.Diagnostics) {
	var out []string
	diags := set.ElementsAs(ctx, &out, false)
	return out, diags
}

func (r *rrsetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan rrsetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	records, diags := recordsOf(ctx, plan.Records)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	rr, err := r.client.CreateRRSet(ctx, plan.Domain.ValueString(), desec.RRSet{
		Subname: plan.Subname.ValueString(),
		Type:    plan.Type.ValueString(),
		TTL:     plan.TTL.ValueInt64(),
		Records: records,
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot create RRset", err.Error())
		return
	}
	plan.setFromServer(rr)
	// Keep the configured values. The server may write them in canonical form.
	resp.Diagnostics.Append(saveServerRecords(ctx, resp.Private, rr.Records)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *rrsetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state rrsetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rr, err := r.client.GetRRSet(ctx, state.Domain.ValueString(), state.Subname.ValueString(), state.Type.ValueString())
	if desec.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read RRset", err.Error())
		return
	}
	state.setFromServer(rr)

	last, ok, diags := loadServerRecords(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	if !ok || !slices.Equal(last, sortedCopy(rr.Records)) {
		// Import, or a change outside of OpenTofu: show the server values.
		set, d := types.SetValueFrom(ctx, types.StringType, rr.Records)
		resp.Diagnostics.Append(d...)
		state.Records = set
		resp.Diagnostics.Append(saveServerRecords(ctx, resp.Private, rr.Records)...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *rrsetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan rrsetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	records, diags := recordsOf(ctx, plan.Records)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	rr, err := r.client.UpdateRRSet(ctx, plan.Domain.ValueString(), plan.Subname.ValueString(), plan.Type.ValueString(),
		plan.TTL.ValueInt64(), records)
	if err != nil {
		resp.Diagnostics.AddError("Cannot update RRset", err.Error())
		return
	}
	plan.setFromServer(rr)
	resp.Diagnostics.Append(saveServerRecords(ctx, resp.Private, rr.Records)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *rrsetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state rrsetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteRRSet(ctx, state.Domain.ValueString(), state.Subname.ValueString(), state.Type.ValueString())
	if err != nil && !desec.IsNotFound(err) {
		resp.Diagnostics.AddError("Cannot delete RRset", err.Error())
	}
}

func (r *rrsetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	domain, subname, typ, err := parseRRSetID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), rrsetID(domain, subname, typ))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("domain"), domain)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("subname"), subname)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("type"), typ)...)
}

// upperCase rejects strings with lower-case letters.
type upperCase struct{}

func (upperCase) Description(context.Context) string               { return "must be upper case" }
func (u upperCase) MarkdownDescription(ctx context.Context) string { return u.Description(ctx) }
func (upperCase) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if v := req.ConfigValue.ValueString(); v != strings.ToUpper(v) {
		resp.Diagnostics.AddAttributeError(req.Path, "Record type must be upper case",
			fmt.Sprintf("Use %q, not %q.", strings.ToUpper(v), v))
	}
}
