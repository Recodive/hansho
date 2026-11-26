package api

import (
	"strings"

	"github.com/recodive/hansho/hub/internal/template"
)

// adminAIPrompt is the text an admin gives an AI assistant to write or repair a template: the rules, the component
// reference (from the validator's own tables), a worked example, the template in the editor and, when there is one,
// the error the hub gave for it. It is the Go port of aiPrompt() in web/app/utils/aiPrompt.ts; the admin has no
// message data to hand over, so that part is left for them to fill in.
func adminAIPrompt(current, errText string) string {
	const fence = "```"
	var ref strings.Builder
	for _, r := range template.Reference() {
		ref.WriteString("- " + r[0] + ": " + r[1] + "\n")
	}
	ex := adminStarters[0]
	var b strings.Builder
	b.WriteString(`You write Hansho templates: JSON that draws a notification, Live Activity or widget on an iPhone from the data of a message.

Reply with one JSON template only, no prose.

Rules
- The root is a vstack or hstack. Every node is {"type": ...}. Unknown properties are errors.
- Placeholders {{a.b.0.c}} read the message data (dot path, numeric array indexes); a missing value renders empty.
- "if": "path" or "!path" on any node drops it unless the value is truthy (falsy with !).
- foreach {"each": "path", "body": node} repeats body per array item; inside, {{item}}, {{item.field}}, {{index}}. No foreach inside foreach.
- Numbers may be a literal or {"bind": "path"}. Colors are {"light": "#RRGGBB", "dark": "#RRGGBB"}.
- Root only: tint and background (colors). Nested stacks may also have background.
- Limits: depth 8, 64 nodes, 512 characters per text, 16 placeholders per string.
- symbol.name is an SF Symbol. image.url is https or a pure {{path}}.

Components and their properties
`)
	b.WriteString(ref.String())
	b.WriteString("\nExample template\n" + fence + "json\n" + strings.TrimSpace(ex.Template) + "\n" + fence + "\n")
	b.WriteString("Its data\n" + fence + "json\n" + strings.TrimSpace(ex.Data) + "\n" + fence)
	if current = strings.TrimSpace(current); current != "" {
		b.WriteString("\n\nMy current template:\n" + fence + "json\n" + current + "\n" + fence)
	}
	b.WriteString("\n\nMy message data (replace this with a real example of the data the message will carry):\n" + fence + "json\n{}\n" + fence)
	if errText != "" {
		b.WriteString("\n\nThe hub rejects my current template with: " + errText + "\nFix that and keep everything else.")
	}
	b.WriteString("\n\nWhat I want: <describe the template here>\n")
	return b.String()
}
