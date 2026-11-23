package provider

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/recodive/hansho/terraform-provider-hansho/internal/client"
)

type templateResource struct{ base }

type templateModel struct {
	ID      types.String         `tfsdk:"id"`
	Body    jsontypes.Normalized `tfsdk:"body"`
	Version types.Int64          `tfsdk:"version"`
}

// NewTemplateResource is hansho_template.
func NewTemplateResource() resource.Resource { return &templateResource{} }

func (r *templateResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "hansho_template"
}

func (r *templateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A template that draws a message or a feed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Required:      true,
				Description:   "Template id, [A-Za-z0-9][A-Za-z0-9_-]{0,63}. Changing it replaces the template.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"body": schema.StringAttribute{
				Required:    true,
				CustomType:  jsontypes.NormalizedType{},
				Description: "The template as JSON, usually jsonencode(...). Formatting and key order do not matter.",
			},
			"version": schema.Int64Attribute{
				Computed:    true,
				Description: "Version on the hub. The provider raises it by one whenever the body changes.",
			},
		},
	}
}

func sameJSON(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

// upsert serves Create and Update. The hub wants every PUT to carry a higher version than the last, so the provider
// reads the current one and sends the next, and sends nothing when the stored body already is the wanted one.
func (r *templateResource) upsert(ctx context.Context, plan tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics) {
	var m templateModel
	if diags.Append(plan.Get(ctx, &m)...); diags.HasError() {
		return
	}
	id, body := m.ID.ValueString(), []byte(m.Body.ValueString())
	cur, err := r.c.GetTemplate(ctx, id)
	if err != nil && !client.IsNotFound(err) {
		diags.AddError("Cannot read template", err.Error())
		return
	}
	m.Version = types.Int64Value(int64(cur.Version))
	if err != nil || !sameJSON(cur.Body, body) {
		next := cur.Version + 1
		if err := r.c.PutTemplate(ctx, id, next, body); err != nil {
			diags.AddError("Cannot save template", err.Error())
			return
		}
		m.Version = types.Int64Value(int64(next))
	}
	diags.Append(state.Set(ctx, m)...)
}

func (r *templateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	r.upsert(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *templateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	r.upsert(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *templateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var id types.String
	if resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...); resp.Diagnostics.HasError() {
		return
	}
	t, err := r.c.GetTemplate(ctx, id.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read template", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, templateModel{ID: id, Body: jsontypes.NewNormalizedValue(string(t.Body)), Version: types.Int64Value(int64(t.Version))})...)
}

func (r *templateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	if resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...); resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.DeleteTemplate(ctx, id.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Cannot delete template", err.Error())
	}
}
