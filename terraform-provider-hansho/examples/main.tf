terraform {
  required_providers {
    hansho = { source = "recodive/hansho" }
  }
}

# endpoint and api_key can also come from HANSHO_ENDPOINT and HANSHO_API_KEY.
provider "hansho" {
  endpoint = "https://hub.example.com"
  api_key  = var.hansho_api_key
}

variable "hansho_api_key" {
  type      = string
  sensitive = true
}

resource "hansho_group" "oncall" {
  id      = "oncall"
  members = ["alice", "bob"]
}

resource "hansho_template" "alert" {
  id = "alert"
  body = jsonencode({
    # whatever the hub's template format asks for
    layout = "stack"
  })
}

resource "hansho_sound" "ding" {
  id           = "ding"
  source       = "${path.module}/ding.caf"
  content_type = "audio/x-caf"
}

resource "hansho_channel" "ops" {
  id            = "ops.alerts"
  name          = "Ops alerts"
  default_level = "active"
  max_level     = "critical"
  sound         = hansho_sound.ding.id
  template_id   = hansho_template.alert.id

  actions = [
    { id = "ack", title = "Acknowledge", ack = true },
    { id = "runbook", title = "Runbook", kind = "url", url = "https://example.com/runbook" },
  ]

  audience = {
    mode   = "only"
    groups = [hansho_group.oncall.id]
  }
}
