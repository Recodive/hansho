// Package provider is the Terraform provider for the Hansho hub's sender API.
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	hansho "github.com/recodive/hansho/sdk/go"
)

type hanshoProvider struct{}

type providerModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	APIKey   types.String `tfsdk:"api_key"`
}

// New returns the provider.
func New() provider.Provider { return &hanshoProvider{} }

func (p *hanshoProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "hansho"
}

func (p *hanshoProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the catalog of a Hansho hub: channels, groups, templates and sounds.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional:    true,
				Description: "https URL of the hub. Defaults to the HANSHO_ENDPOINT environment variable.",
			},
			"api_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Sender API key. Defaults to the HANSHO_API_KEY environment variable.",
			},
		},
	}
}

func (p *hanshoProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	if resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...); resp.Diagnostics.HasError() {
		return
	}
	if cfg.Endpoint.IsUnknown() || cfg.APIKey.IsUnknown() {
		resp.Diagnostics.AddError("Unknown provider value", "endpoint and api_key must be known when the provider is configured.")
		return
	}
	endpoint, key := os.Getenv("HANSHO_ENDPOINT"), os.Getenv("HANSHO_API_KEY")
	if !cfg.Endpoint.IsNull() {
		endpoint = cfg.Endpoint.ValueString()
	}
	if !cfg.APIKey.IsNull() {
		key = cfg.APIKey.ValueString()
	}
	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Missing endpoint", "Set endpoint or the HANSHO_ENDPOINT environment variable.")
	}
	if key == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing API key", "Set api_key or the HANSHO_API_KEY environment variable.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	base, err := checkEndpoint(endpoint, key)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Invalid provider configuration", err.Error())
		return
	}
	resp.ResourceData = hansho.New(base, key) // the SDK's default HTTP client has a 30 second timeout and follows no redirects
}

// checkEndpoint validates the hub URL and the key and returns the URL without a trailing slash. The endpoint must be
// https; plain http is accepted only for a loopback host, so the API key cannot be sent in the clear by mistake.
func checkEndpoint(endpoint, apiKey string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("endpoint must be an https URL such as https://hub.example.com")
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", errors.New("endpoint must use https (http is only accepted for localhost)")
	}
	if apiKey == "" {
		return "", errors.New("api_key must not be empty")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// isNotFound reports whether err is a 404 from the hub.
func isNotFound(err error) bool {
	ae, ok := hansho.IsAPIError(err)
	return ok && ae.Status == http.StatusNotFound
}

// hubError is the text of err for a diagnostic. The SDK's APIError leaves out the hub's validation message, which is
// what tells the user what to fix, so it is put back: "hub answered 400 invalid_request (name): required, ...".
func hubError(err error) string {
	ae, ok := hansho.IsAPIError(err)
	if !ok {
		return err.Error()
	}
	s := fmt.Sprintf("hub answered %d", ae.Status)
	if ae.Code != "" {
		s += " " + ae.Code
	}
	if ae.Field != "" {
		s += " (" + ae.Field + ")"
	}
	if ae.Message != "" {
		s += ": " + ae.Message
	}
	return s
}

func (p *hanshoProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewChannelResource, NewGroupResource, NewTemplateResource, NewSoundResource}
}

func (p *hanshoProvider) DataSources(context.Context) []func() datasource.DataSource { return nil }

// base holds what every resource shares: the client, and the hub's id as the import id.
type base struct{ c *hansho.Client }

func (b *base) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	if req.ProviderData != nil { // nil while Terraform only validates the configuration
		b.c = req.ProviderData.(*hansho.Client)
	}
}

func (b *base) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func nullIfEmpty(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// strs and tfStrs convert between Terraform set elements and plain strings; the results are never nil, so an empty list
// stays an empty set instead of becoming null.
func strs(in []types.String) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, s.ValueString())
	}
	return out
}

func tfStrs(in []string) []types.String {
	out := make([]types.String, 0, len(in))
	for _, s := range in {
		out = append(out, types.StringValue(s))
	}
	return out
}
