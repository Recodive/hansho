package provider

import (
	"context"

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

type groupResource struct{ base }

type groupModel struct {
	ID      types.String   `tfsdk:"id"`
	Members []types.String `tfsdk:"members"`
}

// NewGroupResource is hansho_group.
func NewGroupResource() resource.Resource { return &groupResource{} }

func (r *groupResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "hansho_group"
}

func (r *groupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A group of users that audiences can name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Required:      true,
				Description:   "Group id, [A-Za-z0-9][A-Za-z0-9_-]{0,63}. Changing it replaces the group.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"members": schema.SetAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "External user ids; they may not have paired yet. Replaces the whole member list.",
			},
		},
	}
}

// upsert serves Create and Update: the hub's PUT replaces the member list either way.
func (r *groupResource) upsert(ctx context.Context, plan tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics) {
	var m groupModel
	if diags.Append(plan.Get(ctx, &m)...); diags.HasError() {
		return
	}
	members, err := r.c.PutGroup(ctx, m.ID.ValueString(), strs(m.Members))
	if err != nil {
		diags.AddError("Cannot save group", err.Error())
		return
	}
	m.Members = tfStrs(members)
	diags.Append(state.Set(ctx, m)...)
}

func (r *groupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	r.upsert(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *groupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	r.upsert(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *groupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var id types.String
	if resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...); resp.Diagnostics.HasError() {
		return
	}
	members, err := r.c.GetGroup(ctx, id.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read group", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, groupModel{ID: id, Members: tfStrs(members)})...)
}

func (r *groupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	if resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...); resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.DeleteGroup(ctx, id.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Cannot delete group", err.Error())
	}
}
