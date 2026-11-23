package api

import (
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
)

// adminSetting is one row of the settings page: what is in effect and the environment variable that sets it.
type adminSetting struct {
	Name, Value string
	Env         []string
	Note        string
	Logo        bool // show the logo under the value
}

// adminBuild describes the binary: the commit it was built from when Go recorded one, else its module version, else "".
func adminBuild() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev, dirty string
	for _, kv := range bi.Settings {
		switch kv.Key {
		case "vcs.revision":
			rev = kv.Value
		case "vcs.modified":
			if kv.Value == "true" {
				dirty = ", with local changes"
			}
		}
	}
	switch {
	case rev != "":
		return "commit " + rev[:min(len(rev), 12)] + dirty + " (" + bi.GoVersion + ")"
	case bi.Main.Version != "" && bi.Main.Version != "(devel)":
		return bi.Main.Version + " (" + bi.GoVersion + ")"
	}
	return ""
}

func adminOnOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

// adminSettingsList is what is in effect. It holds no secret: a secret is only said to be set.
func (s *server) adminSettingsList() []adminSetting {
	c := s.Cfg
	set := func(v string) string {
		if v == "" {
			return "Not set"
		}
		return v
	}
	logo := adminSetting{Name: "Logo", Value: "Not set", Env: []string{"HUB_LOGO_FILE", "HUB_LOGO_URL"}, Note: "A PNG, 64 to 1024 pixels each way, at most 256 KB. Without one the app shows the first letter of the name."}
	if n := len(c.Logo); n > 0 {
		logo.Value, logo.Logo = "Set (PNG, "+adminSize(n)+")", true
	}
	webhook := adminSetting{Name: "Webhook address", Value: "Not set", Env: []string{"HUB_WEBHOOK_URL"}, Note: "Where the hub reports events to your software. Only the host is shown here."}
	if c.WebhookURL != "" {
		webhook.Value = "Not shown"
		if u, err := url.Parse(c.WebhookURL); err == nil && u.Hostname() != "" {
			webhook.Value = u.Hostname()
		}
	}
	secret := "Not set"
	if len(c.WebhookSecret) > 0 {
		secret = "Set (never shown)"
	}
	build := adminBuild()
	if build == "" {
		build = "Not recorded in this build"
	}
	return []adminSetting{
		{Name: "Display name", Value: c.DisplayName, Env: []string{"HUB_DISPLAY_NAME"}, Note: "What people see in the app as the sender."},
		{Name: "Public address", Value: c.PublicURL, Env: []string{"HUB_PUBLIC_URL"}, Note: "The address of this hub on the internet. Pair codes and the app use it."},
		{Name: "Hub id", Value: set(c.HubID), Env: []string{"HUB_ID"}, Note: "Given by the relay when the hub was registered."},
		{Name: "Relay", Value: c.RelayURL, Env: []string{"HUB_RELAY_URL"}, Note: "The service that hands notifications to Apple."},
		{Name: "Project site", Value: s.homeURL(), Env: []string{"HUB_HOME_URL"}, Note: "Where the hub's status page and the template builder link to."},
		logo,
		webhook,
		{Name: "Webhook signing secret", Value: secret, Env: []string{"HUB_WEBHOOK_SECRET"}, Note: "Lets your software check that a webhook came from this hub."},
		{Name: "Encrypted Live Activities", Value: adminOnOff(c.LAEncrypt), Env: []string{"HUB_LA_ENCRYPT"}, Note: "When on, the content of a Live Activity is encrypted on its way to the phone."},
		{Name: "Admin pages", Value: adminOnOff(c.Admin), Env: []string{"HUB_ADMIN", "HUB_ADMIN_PASSWORD"}, Note: "These pages. The password is never shown."},
		{Name: "Pairing fingerprint", Value: strings.Join(groups(s.fp), " "), Env: []string{"HUB_KEY_PATH"}, Note: "Identifies this hub's key. People can compare it with the one the app shows. It comes from the key file, which stays as it is."},
		{Name: "Build", Value: build, Note: "The program this hub is running."},
	}
}

func (s *server) adminSettings(w http.ResponseWriter, _ *http.Request, csrf string) {
	s.adminRender(w, http.StatusOK, "settings", "settings", "Settings", csrf, s.adminSettingsList())
}
