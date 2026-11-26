# Terraform provider for Hansho

Manages the catalog of a [Hansho hub](../hub/README.md) from Terraform: channels, groups, templates and sounds. It
talks to the hub's sender API (`/api/v1`) with a sender API key (`hub apikey create <name>`). The hub must offer the
`GET` and `DELETE` endpoints for channels, groups, templates and sounds.

Not published anywhere yet. Build it with `make build` and use it through `dev_overrides` (below).

## Usage

```hcl
terraform {
  required_providers {
    hansho = { source = "recodive/hansho" }
  }
}

provider "hansho" {
  endpoint = "https://hub.example.com" # or HANSHO_ENDPOINT
  api_key  = var.hansho_api_key        # or HANSHO_API_KEY
}

resource "hansho_group" "oncall" {
  id      = "oncall"
  members = ["alice", "bob"]
}

resource "hansho_channel" "ops" {
  id            = "ops.alerts"
  name          = "Ops alerts"
  default_level = "active"
  max_level     = "critical"

  audience = {
    mode   = "only"
    groups = [hansho_group.oncall.id]
  }
}
```

A fuller example is in [examples/main.tf](examples/main.tf).

Every resource is a PUT to the hub, so creating one that already exists overwrites it. Import with the id:
`terraform import hansho_channel.ops ops.alerts`. A resource deleted on the hub is dropped from the state and planned
for creation again.

## Provider

| Attribute | |
|---|---|
| `endpoint` | https URL of the hub (plain http only for localhost). Env `HANSHO_ENDPOINT` |
| `api_key` | Sender API key, sensitive. Env `HANSHO_API_KEY` |

## `hansho_channel`

| Attribute | |
|---|---|
| `id` | Required. `[A-Za-z0-9][A-Za-z0-9._-]{0,63}`. Changing it replaces the channel |
| `name` | Required |
| `default_level`, `max_level` | Required. `passive`, `active`, `time-sensitive`, `critical` or `page`; the default must not exceed the max |
| `sound` | Id of a `hansho_sound` |
| `template_id` | Id of a `hansho_template` |
| `thread` | Computed unless set; the hub uses the channel id |
| `actions` | List of buttons: `id`, `title` (required), `kind`, `url` (https), `ack` |
| `audience` | `mode` (`everyone`, `only`, `except`), `users` and `groups` (sets; groups must exist). Everyone when left out |

## `hansho_group`

| Attribute | |
|---|---|
| `id` | Required, `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`. Changing it replaces the group |
| `members` | Required set of external user ids; replaces the whole list |

## `hansho_template`

| Attribute | |
|---|---|
| `id` | Required, same pattern as a group's. Changing it replaces the template |
| `body` | Required JSON string, usually `jsonencode(...)`. Formatting and key order are not changes |
| `version` | Computed. The hub wants it to rise on every change, so the provider reads the current one and sends the next, and only when the body really changed |

## `hansho_sound`

| Attribute | |
|---|---|
| `id` | Required, same pattern as a group's. Changing it replaces the sound |
| `source` | Path of the audio file. Exactly one of `source` and `content_base64` |
| `content_base64` | The audio, base64 encoded |
| `content_type` | Required. `audio/x-caf`, `audio/wav` or `audio/aiff` (at most 1 MB; WAV and AIFF under 30 s) |
| `sha256`, `bytes` | Computed. A changed file changes `sha256` and is uploaded again |

After an import `source` and `content_base64` are unset, so the first apply uploads the file once more.

## Development

```sh
make build   # ./terraform-provider-hansho
make test    # go test; runs the terraform binary on PATH against a fake hub, no network or hub needed
make vet
```

To try the build, point a `~/.terraformrc` at it and skip `terraform init`:

```hcl
provider_installation {
  dev_overrides { "recodive/hansho" = "/path/to/terraform-provider-hansho" } # the directory holding the binary
  direct {}
}
```
