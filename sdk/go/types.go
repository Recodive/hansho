package hansho

import "encoding/json"

// The types, enums and event constants are generated from sdk/spec/openapi.json into types_gen.go.

// Valid reports whether l is one of the five known levels.
func (l Level) Valid() bool {
	switch l {
	case LevelPassive, LevelActive, LevelTimeSensitive, LevelCritical, LevelPage:
		return true
	}
	return false
}

// RecipientNoAccess is the Status of a recipient outside the channel's audience: it got no message.
const RecipientNoAccess = "no_access"

type templateRequest struct {
	Version int             `json:"version"`
	Body    json.RawMessage `json:"body"`
}
