// Package brand holds the single constant from which every user-visible
// identifier (URL scheme, header prefix, well-known path) derives. The
// codename is temporary; change it here only.
package brand

import "strings"

// Name is the product codename.
const Name = "hansho"

// Scheme is the URL scheme of pairing links, e.g. "hansho".
const Scheme = Name

// HeaderPrefix is the prefix for all custom HTTP headers, e.g. "X-Hansho-".
func HeaderPrefix() string {
	return "X-" + strings.ToUpper(Name[:1]) + Name[1:] + "-"
}

// Header returns the full header name for suffix, e.g. Header("Hub") == "X-Hansho-Hub".
func Header(suffix string) string { return HeaderPrefix() + suffix }

// ChallengePath is the well-known path a hub serves its domain challenge on.
const ChallengePath = "/.well-known/" + Name + "-challenge"

// Crypto domain-separation labels, e.g. Label("v1/wrap") == "hansho/v1/wrap".
func Label(s string) string { return Name + "/" + s }
