package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/recodive/hansho/terraform-provider-hansho/internal/client"
)

type channelResource struct{ base }

type actionModel struct {
	ID    types.String `tfsdk:"id"`
	Title types.String `tfsdk:"title"`
	Kind  types.String `tfsdk:"kind"`
	URL   types.String `tfsdk:"url"`
	Ack   types.Bool   `tfsdk:"ack"`
}

type audienceModel struct {
	Mode   types.String   `tfsdk:"mode"`
	Users  []types.String `tfsdk:"users"`
	Groups []types.String `tfsdk:"groups"`
}

type channelModel struct {
	ID           types.String  `tfsdk:"id"`
	Name         types.String  `tfsdk:"name"`
	DefaultLevel types.String  `tfsdk:"default_level"`
	MaxLevel     types.String  `tfsdk:"max_level"`
	Sound        types.String  `tfsdk:"sound"`
	TemplateID   types.String  `tfsdk:"template_id"`
	Thread       types.String  `tfsdk:"thread"`
	Actions      []actionModel `tfsdk:"actions"`
	Audience     audienceModel `tfsdk:"audience"`
}

var (
	actionType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"id": types.StringType, "title": types.StringType, "kind": types.StringType, "url": types.StringType, "ack": types.BoolType,
	}}
	audienceAttrTypes = map[string]attr.Type{
		"mode": types.StringType, "users": types.SetType{ElemType: types.StringType}, "groups": types.SetType{ElemType: types.StringType},
	}
)

// NewChannelResource is hansho_channel.
func NewChannelResource() resource.Resource { return &channelResource{} }

func (r *channelResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "hansho_channel"
}

func (r *channelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	emptySet := setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{}))
	resp.Schema = schema.Schema{
		Description: "A channel messages are sent to.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Required:      true,
				Description:   "Channel id, [A-Za-z0-9][A-Za-z0-9._-]{0,63}. Changing it replaces the channel.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name":          schema.StringAttribute{Required: true, Description: "Display name, at most 256 bytes."},
			"default_level": schema.StringAttribute{Required: true, Description: "Level of messages that name none: passive, active, time-sensitive, critical or page. Must not exceed max_level."},
			"max_level":     schema.StringAttribute{Required: true, Description: "Highest level the channel allows: passive, active, time-sensitive, critical or page."},
			"sound":         schema.StringAttribute{Optional: true, Description: "Id of a hansho_sound."},
			"template_id":   schema.StringAttribute{Optional: true, Description: "Id of a hansho_template."},
			"thread": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Thread id notifications are grouped under. The hub uses the channel id when unset.",
			},
			"actions": schema.ListNestedAttribute{
				Optional: true, Computed: true,
				Default:     listdefault.StaticValue(types.ListValueMust(actionType, []attr.Value{})),
				Description: "Buttons every message of the channel gets.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":    schema.StringAttribute{Required: true, Description: "Action id."},
					"title": schema.StringAttribute{Required: true, Description: "Button title."},
					"kind":  schema.StringAttribute{Optional: true, Description: "\"url\" opens url; \"text\" asks for a reply."},
					"url":   schema.StringAttribute{Optional: true, Description: "https URL, for kind \"url\"."},
					"ack":   schema.BoolAttribute{Optional: true, Description: "Pressing the button acknowledges the message."},
				}},
			},
			"audience": schema.SingleNestedAttribute{
				Optional: true, Computed: true,
				Default: objectdefault.StaticValue(types.ObjectValueMust(audienceAttrTypes, map[string]attr.Value{
					"mode": types.StringValue("everyone"), "users": types.SetValueMust(types.StringType, []attr.Value{}), "groups": types.SetValueMust(types.StringType, []attr.Value{}),
				})),
				Description: "Who may use the channel. Leaving it out means everyone.",
				Attributes: map[string]schema.Attribute{
					"mode":   schema.StringAttribute{Required: true, Description: "everyone, only (the listed users and groups) or except (everyone but them)."},
					"users":  schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet, Description: "External user ids. Empty for everyone."},
					"groups": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet, Description: "Ids of hansho_group resources, which must exist. Empty for everyone."},
				},
			},
		},
	}
}

func (m channelModel) toAPI() client.Channel {
	ch := client.Channel{
		Name: m.Name.ValueString(), DefaultLevel: m.DefaultLevel.ValueString(), MaxLevel: m.MaxLevel.ValueString(),
		Sound: m.Sound.ValueString(), TemplateID: m.TemplateID.ValueString(), Thread: m.Thread.ValueString(),
		Actions:  make([]client.Action, 0, len(m.Actions)),
		Audience: client.Audience{Mode: m.Audience.Mode.ValueString(), Users: strs(m.Audience.Users), Groups: strs(m.Audience.Groups)},
	}
	for _, a := range m.Actions {
		ch.Actions = append(ch.Actions, client.Action{ID: a.ID.ValueString(), Title: a.Title.ValueString(),
			Kind: a.Kind.ValueString(), URL: a.URL.ValueString(), Ack: a.Ack.ValueBoolPointer()})
	}
	return ch
}

func channelFromAPI(id types.String, ch client.Channel) channelModel {
	m := channelModel{
		ID: id, Name: types.StringValue(ch.Name), DefaultLevel: types.StringValue(ch.DefaultLevel), MaxLevel: types.StringValue(ch.MaxLevel),
		Sound: nullIfEmpty(ch.Sound), TemplateID: nullIfEmpty(ch.TemplateID), Thread: types.StringValue(ch.Thread),
		Actions:  make([]actionModel, 0, len(ch.Actions)),
		Audience: audienceModel{Mode: types.StringValue(ch.Audience.Mode), Users: tfStrs(ch.Audience.Users), Groups: tfStrs(ch.Audience.Groups)},
	}
	if ch.Audience.Mode == "" {
		m.Audience.Mode = types.StringValue("everyone")
	}
	for _, a := range ch.Actions {
		act := actionModel{ID: types.StringValue(a.ID), Title: types.StringValue(a.Title), Kind: nullIfEmpty(a.Kind), URL: nullIfEmpty(a.URL), Ack: types.BoolPointerValue(a.Ack)}
		m.Actions = append(m.Actions, act)
	}
	return m
}

// upsert serves Create and Update: the hub's PUT replaces the whole channel either way.
func (r *channelResource) upsert(ctx context.Context, plan tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics) {
	var m channelModel
	if diags.Append(plan.Get(ctx, &m)...); diags.HasError() {
		return
	}
	ch, err := r.c.PutChannel(ctx, m.ID.ValueString(), m.toAPI())
	if err != nil {
		diags.AddError("Cannot save channel", err.Error())
		return
	}
	diags.Append(state.Set(ctx, channelFromAPI(m.ID, ch))...)
}

func (r *channelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	r.upsert(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *channelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	r.upsert(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *channelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var id types.String
	if resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...); resp.Diagnostics.HasError() {
		return
	}
	ch, err := r.c.GetChannel(ctx, id.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read channel", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, channelFromAPI(id, ch))...)
}

func (r *channelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	if resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...); resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.DeleteChannel(ctx, id.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Cannot delete channel", err.Error())
	}
}
