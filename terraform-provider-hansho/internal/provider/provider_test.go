package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var factories = map[string]func() (tfprotov6.ProviderServer, error){
	"hansho": providerserver.NewProtocol6WithError(New()),
}

// setup starts a fake hub, points the provider at it and returns the hub and the provider block. These tests run the
// real terraform binary (TF_ACC_TERRAFORM_PATH, else the one on PATH) and are skipped without one, because the test
// framework would otherwise try to download it.
func setup(t *testing.T) (*fakeHub, string) {
	t.Helper()
	if os.Getenv("TF_ACC_TERRAFORM_PATH") == "" {
		p, err := exec.LookPath("terraform")
		if err != nil {
			t.Skip("no terraform binary on PATH and TF_ACC_TERRAFORM_PATH is not set")
		}
		t.Setenv("TF_ACC_TERRAFORM_PATH", p)
	}
	hub, url := newFakeHub(t, "test-key")
	return hub, fmt.Sprintf("provider \"hansho\" {\n  endpoint = %q\n  api_key  = \"test-key\"\n}\n", url)
}

func TestChannel(t *testing.T) {
	hub, prov := setup(t)
	cfg := func(body string) string {
		return prov + `
resource "hansho_group" "oncall" {
  id      = "oncall"
  members = ["alice"]
}
resource "hansho_channel" "ops" {
  id            = "ops.alerts"
  default_level = "active"
  max_level     = "critical"
` + body + "\n}\n"
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			if len(hub.channels) != 0 || len(hub.groups) != 0 {
				return fmt.Errorf("left behind: %d channels, %d groups", len(hub.channels), len(hub.groups))
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg(`  name = "Ops"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hansho_channel.ops", "thread", "ops.alerts"),
					resource.TestCheckResourceAttr("hansho_channel.ops", "audience.mode", "everyone"),
					resource.TestCheckResourceAttr("hansho_channel.ops", "actions.#", "0"),
					resource.TestCheckResourceAttr("hansho_group.oncall", "members.#", "1"),
				),
			},
			{
				Config: cfg(`
  name = "Operations"
  thread = "ops"
  actions = [
    { id = "ack", title = "Acknowledge", ack = true },
    { id = "runbook", title = "Runbook", kind = "url", url = "https://example.com/runbook" },
  ]
  audience = {
    mode   = "only"
    groups = [hansho_group.oncall.id]
  }
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hansho_channel.ops", "name", "Operations"),
					resource.TestCheckResourceAttr("hansho_channel.ops", "thread", "ops"),
					resource.TestCheckResourceAttr("hansho_channel.ops", "actions.#", "2"),
					resource.TestCheckResourceAttr("hansho_channel.ops", "actions.1.kind", "url"),
					resource.TestCheckResourceAttr("hansho_channel.ops", "audience.groups.#", "1"),
				),
			},
			{ResourceName: "hansho_channel.ops", ImportState: true, ImportStateVerify: true},
			{ // a channel deleted behind Terraform's back is planned for creation again
				PreConfig: func() { delete(hub.channels, "ops.alerts") },
				Config:    cfg(`  name = "Operations"`),
				Check:     resource.TestCheckResourceAttr("hansho_channel.ops", "audience.mode", "everyone"),
			},
			{ // the hub's validation message reaches the user
				Config:      cfg(`  name = ""`),
				ExpectError: regexp.MustCompile(`(?s)400 invalid_request \(name\).*required, at most 256 bytes`),
			},
		},
	})
}

// ack = false has to survive the round trip: the SDK drops a false ack from the PUT, so the provider keeps the planned
// false, and neither the apply nor the next plan may differ from the configuration.
func TestChannelAckFalse(t *testing.T) {
	hub, prov := setup(t)
	cfg := func(actions string) string {
		return prov + `
resource "hansho_channel" "c" {
  id            = "ack"
  name          = "Ack"
  default_level = "active"
  max_level     = "page"
  actions       = [` + actions + `]
}
`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: cfg(`{ id = "a", title = "A", ack = false }, { id = "b", title = "B" }, { id = "c", title = "C", ack = true }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hansho_channel.c", "actions.0.ack", "false"),
					resource.TestCheckNoResourceAttr("hansho_channel.c", "actions.1.ack"),
					resource.TestCheckResourceAttr("hansho_channel.c", "actions.2.ack", "true"),
					func(*terraform.State) error { // the audience is sent even when the configuration leaves it out
						var c struct {
							Audience struct{ Mode string }
						}
						if err := json.Unmarshal(hub.channels["ack"], &c); err != nil || c.Audience.Mode != "everyone" {
							return fmt.Errorf("stored channel %s: audience mode %q, %v", hub.channels["ack"], c.Audience.Mode, err)
						}
						return nil
					}),
			},
			{ // a refresh reads the same, and drift of the ack to true on the hub is seen
				PreConfig: func() {
					hub.channels["ack"] = json.RawMessage(strings.Replace(string(hub.channels["ack"]), `"id":"a"`, `"id":"a","ack":true`, 1))
				},
				Config: cfg(`{ id = "a", title = "A", ack = false }, { id = "b", title = "B" }, { id = "c", title = "C", ack = true }`),
				Check:  resource.TestCheckResourceAttr("hansho_channel.c", "actions.0.ack", "false"),
			},
		},
	})
}

func TestGroup(t *testing.T) {
	_, prov := setup(t)
	cfg := func(members string) string {
		return prov + `resource "hansho_group" "g" {
  id      = "g1"
  members = ` + members + "\n}\n"
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: cfg(`["alice", "bob"]`), Check: resource.TestCheckResourceAttr("hansho_group.g", "members.#", "2")},
			{Config: cfg(`[]`), Check: resource.TestCheckResourceAttr("hansho_group.g", "members.#", "0")},
			{ResourceName: "hansho_group.g", ImportState: true, ImportStateVerify: true},
		},
	})
}

func TestTemplate(t *testing.T) {
	hub, prov := setup(t)
	cfg := func(body string) string {
		return prov + `resource "hansho_template" "t" {
  id   = "card"
  body = ` + body + "\n}\n"
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: cfg(`jsonencode({ a = 1, b = [1, 2] })`), Check: resource.TestCheckResourceAttr("hansho_template.t", "version", "1")},
			{ // the same JSON, written differently, is no change and sends no PUT
				Config: cfg(`"{\"b\": [1, 2],\n \"a\": 1}"`),
				Check: func(*terraform.State) error {
					if n := hub.puts["templates/card"]; n != 1 {
						return fmt.Errorf("%d PUTs, want 1", n)
					}
					return nil
				},
			},
			{Config: cfg(`jsonencode({ a = 2 })`), Check: resource.TestCheckResourceAttr("hansho_template.t", "version", "2")},
			{ResourceName: "hansho_template.t", ImportState: true, ImportStateVerify: true},
			{ // someone else raised the version: the next change continues from the hub's number
				PreConfig: func() { hub.templates["card"] = fakeTemplate{Version: 7, Body: hub.templates["card"].Body} },
				Config:    cfg(`jsonencode({ a = 3 })`),
				Check:     resource.TestCheckResourceAttr("hansho_template.t", "version", "8"),
			},
		},
	})
}

func TestSound(t *testing.T) {
	hub, prov := setup(t)
	file := filepath.Join(t.TempDir(), "ding.caf")
	write := func(s string) { os.WriteFile(file, []byte(s), 0o600) }
	write("caff-one")
	cfgFile := prov + fmt.Sprintf(`resource "hansho_sound" "s" {
  id           = "ding"
  source       = %q
  content_type = "audio/x-caf"
}
`, file)
	cfgB64 := prov + fmt.Sprintf(`resource "hansho_sound" "s" {
  id             = "ding"
  content_base64 = %q
  content_type   = "audio/x-caf"
}
`, base64.StdEncoding.EncodeToString([]byte("caff-b64")))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: prov + `resource "hansho_sound" "s" {
  id           = "ding"
  content_type = "audio/mp3"
}`,
				ExpectError: regexp.MustCompile(`(?s)Exactly one of source and content_base64|Invalid content_type`),
			},
			{Config: cfgFile, Check: resource.TestCheckResourceAttr("hansho_sound.s", "bytes", "8")},
			{ // the file changes and the path does not: the new content is still uploaded
				PreConfig: func() { write("caff-two-longer") },
				Config:    cfgFile,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hansho_sound.s", "bytes", "15"),
					func(*terraform.State) error {
						if hub.sounds["ding"]["bytes"] != 15 {
							return fmt.Errorf("hub has %v bytes", hub.sounds["ding"]["bytes"])
						}
						return nil
					}),
			},
			{Config: cfgB64, Check: resource.TestCheckResourceAttr("hansho_sound.s", "bytes", "8")},
			{ResourceName: "hansho_sound.s", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"content_base64", "source"}},
		},
	})
}

func TestProviderConfig(t *testing.T) {
	_, _ = setup(t)
	group := "resource \"hansho_group\" \"g\" {\n  id      = \"g\"\n  members = []\n}\n"
	badEndpoint := "provider \"hansho\" {\n  endpoint = \"http://hub.example.com\"\n  api_key  = \"k\"\n}\n" + group
	noEndpoint := "provider \"hansho\" {\n  api_key = \"k\"\n}\n" + group
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      badEndpoint,
			ExpectError: regexp.MustCompile(`must use https`),
		}, {
			Config:      noEndpoint,
			ExpectError: regexp.MustCompile(`Missing endpoint`),
		}},
	})
}
