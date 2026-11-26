package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type soundResource struct{ base }

type soundModel struct {
	ID            types.String `tfsdk:"id"`
	Source        types.String `tfsdk:"source"`
	ContentBase64 types.String `tfsdk:"content_base64"`
	ContentType   types.String `tfsdk:"content_type"`
	SHA256        types.String `tfsdk:"sha256"`
	Bytes         types.Int64  `tfsdk:"bytes"`
}

// NewSoundResource is hansho_sound.
func NewSoundResource() resource.Resource { return &soundResource{} }

func (r *soundResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "hansho_sound"
}

func (r *soundResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A notification sound: at most 1 MB, and under 30 seconds for WAV and AIFF.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Required:      true,
				Description:   "Sound id, [A-Za-z0-9][A-Za-z0-9_-]{0,63}. Changing it replaces the sound.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"source":         schema.StringAttribute{Optional: true, Description: "Path of the audio file. Exactly one of source and content_base64 is required."},
			"content_base64": schema.StringAttribute{Optional: true, Description: "The audio, base64 encoded."},
			"content_type":   schema.StringAttribute{Required: true, Description: "audio/x-caf, audio/wav or audio/aiff."},
			"sha256":         schema.StringAttribute{Computed: true, Description: "SHA-256 of the audio, in hex. A change of the file shows up as a change of this."},
			"bytes":          schema.Int64Attribute{Computed: true, Description: "Size of the audio."},
		},
	}
}

func (r *soundResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m soundModel
	if resp.Diagnostics.Append(req.Config.Get(ctx, &m)...); resp.Diagnostics.HasError() {
		return
	}
	if m.Source.IsNull() == m.ContentBase64.IsNull() {
		resp.Diagnostics.AddError("Invalid sound", "Exactly one of source and content_base64 must be set.")
	}
	switch m.ContentType.ValueString() {
	case "audio/x-caf", "audio/wav", "audio/aiff":
	default:
		if !m.ContentType.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root("content_type"), "Invalid content_type", "Must be audio/x-caf, audio/wav or audio/aiff.")
		}
	}
}

// audio returns the sound's bytes from the file or from the base64 text.
func (m soundModel) audio() ([]byte, error) {
	if !m.Source.IsNull() {
		return os.ReadFile(m.Source.ValueString())
	}
	return base64.StdEncoding.DecodeString(m.ContentBase64.ValueString())
}

// ModifyPlan puts the audio's hash and size into the plan, so that a changed file is a change Terraform sees.
func (r *soundResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var m soundModel
	if resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...); resp.Diagnostics.HasError() {
		return
	}
	if m.Source.IsUnknown() || m.ContentBase64.IsUnknown() || m.Source.IsNull() == m.ContentBase64.IsNull() {
		return
	}
	data, err := m.audio()
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the sound", err.Error())
		return
	}
	sum := sha256.Sum256(data)
	m.SHA256, m.Bytes = types.StringValue(hex.EncodeToString(sum[:])), types.Int64Value(int64(len(data)))
	resp.Diagnostics.Append(resp.Plan.Set(ctx, m)...)
}

// upsert serves Create and Update: the hub's PUT replaces the sound either way.
func (r *soundResource) upsert(ctx context.Context, plan tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics) {
	var m soundModel
	if diags.Append(plan.Get(ctx, &m)...); diags.HasError() {
		return
	}
	data, err := m.audio()
	if err != nil {
		diags.AddError("Cannot read the sound", err.Error())
		return
	}
	res, err := r.c.PutSound(ctx, m.ID.ValueString(), m.ContentType.ValueString(), bytes.NewReader(data))
	if err != nil {
		diags.AddError("Cannot save sound", hubError(err))
		return
	}
	m.SHA256, m.Bytes = types.StringValue(res.SHA256), types.Int64Value(int64(res.Size))
	diags.Append(state.Set(ctx, m)...)
}

func (r *soundResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	r.upsert(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *soundResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	r.upsert(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *soundResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m soundModel
	if resp.Diagnostics.Append(req.State.Get(ctx, &m)...); resp.Diagnostics.HasError() {
		return
	}
	s, err := r.c.GetSound(ctx, m.ID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read sound", hubError(err))
		return
	}
	m.ContentType, m.SHA256, m.Bytes = types.StringValue(s.ContentType), types.StringValue(s.SHA256), types.Int64Value(int64(s.Bytes))
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *soundResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	if resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...); resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.DeleteSound(ctx, id.ValueString()); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Cannot delete sound", hubError(err))
	}
}
