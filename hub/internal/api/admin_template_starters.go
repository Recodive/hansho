package api

// adminStarter is a template to start from, with the message data it is drawn with. The starters are the examples of
// the web template builder (web/app/utils/templateExamples.ts), copied as they are; a test checks that each is valid.
type adminStarter struct {
	ID, Label, About, Template, Data string
}

var adminStarters = []adminStarter{
	{
		ID:    "deploys",
		Label: "Deploys list",
		About: "A header and a foreach: one row per element of the items array.",
		Template: `{"type":"vstack","spacing":10,"align":"leading","children":[
  {"type":"hstack","spacing":8,"children":[
    {"type":"symbol","name":"shippingbox.fill","size":20},
    {"type":"text","text":"{{title}}","style":"headline"},
    {"type":"spacer"},
    {"type":"badge","text":"{{env}}"}]},
  {"type":"foreach","each":"items","max":5,"spacing":6,"body":
    {"type":"hstack","spacing":8,"children":[
      {"type":"text","text":"{{item.name}}"},
      {"type":"spacer"},
      {"type":"text","text":"{{item.status}}","style":"caption"}]}}]}`,
		Data: `{
  "title": "Deploys",
  "env": "prod",
  "items": [
    {"name": "web 4.14.0", "status": "ok"},
    {"name": "api 2.9.1", "status": "ok"},
    {"name": "worker 1.3.0", "status": "slow"},
    {"name": "cron 0.8.2", "status": "ok"}
  ]
}`,
	},
	{
		ID:    "order",
		Label: "Order progress",
		About: "A progress bar bound to a number, a ticking countdown and a badge.",
		Template: `{"type":"vstack","spacing":10,"align":"leading","children":[
  {"type":"hstack","spacing":8,"children":[
    {"type":"symbol","name":"shippingbox.fill","size":22},
    {"type":"text","text":"Order {{order}}","style":"headline"},
    {"type":"spacer"},
    {"type":"badge","text":"{{status}}","symbol":"truck.box.fill","color":{"light":"#1A7F37","dark":"#4CD964"}}]},
  {"type":"progress","value":{"bind":"progress"},"label":"{{step}}"},
  {"type":"hstack","spacing":6,"children":[
    {"type":"text","text":"Arrives in","style":"footnote"},
    {"type":"timer","to":{"bind":"arrives_at"},"style":"footnote"},
    {"type":"spacer"},
    {"type":"text","text":"{{carrier}}","style":"footnote","color":{"light":"#6C6C70","dark":"#AEAEB2"}}]}]}`,
		Data: `{
  "order": "#48213",
  "status": "Out for delivery",
  "step": "Packed, shipped, out for delivery",
  "progress": 0.75,
  "arrives_at": 1795377600,
  "carrier": "Parcel Co"
}`,
	},
	{
		ID:    "incident",
		Label: "Incident",
		About: "A symbol, a severity badge, a gauge and a tint colour for the whole card.",
		Template: `{"type":"vstack","spacing":10,"align":"leading",
 "tint":{"light":"#C93400","dark":"#FF9F0A"},"children":[
  {"type":"hstack","spacing":10,"align":"leading","children":[
    {"type":"symbol","name":"exclamationmark.triangle.fill","size":26},
    {"type":"vstack","spacing":2,"children":[
      {"type":"text","text":"{{service}}","style":"headline"},
      {"type":"text","text":"{{summary}}","style":"footnote","lines":2}]},
    {"type":"spacer"},
    {"type":"badge","text":"{{severity}}"}]},
  {"type":"gauge","value":{"bind":"error_rate"},"min":0,"max":100,"label":"Error rate {{error_rate}}%"},
  {"type":"text","text":"Started {{started}} · {{responders}} responders","style":"caption"}]}`,
		Data: `{
  "service": "payments-api",
  "summary": "Checkout requests fail with 502 from the eu-west gateway.",
  "severity": "SEV2",
  "error_rate": 37.5,
  "started": "14:02",
  "responders": 3
}`,
	},
	{
		ID:    "oncall",
		Label: "On-call card",
		About: "Padding, a filled panel, text weight, a divider, and if to leave things out.",
		Template: `{"type":"vstack","spacing":10,"align":"leading",
 "tint":{"light":"#0A84FF","dark":"#64D2FF"},"children":[
  {"type":"hstack","spacing":8,"children":[
    {"type":"symbol","name":"phone.badge.waveform.fill","size":20},
    {"type":"text","text":"{{title}}","style":"headline","weight":"bold","lines":2},
    {"type":"spacer","min":8},
    {"type":"badge","text":"{{severity}}","if":"severity",
     "color":{"light":"#C93400","dark":"#FF9F0A"}}]},
  {"type":"vstack","spacing":2,"padding":12,"radius":12,"align":"leading","if":"primary.name",
   "background":{"light":"#FFFFFF","dark":"#3A3A3C"},"children":[
    {"type":"text","text":"On call now","style":"caption","weight":"medium",
     "color":{"light":"#6C6C70","dark":"#AEAEB2"}},
    {"type":"text","text":"{{primary.name}}","style":"headline","weight":"semibold"},
    {"type":"text","text":"{{primary.phone}} · until {{primary.until}}","style":"footnote",
     "color":{"light":"#6C6C70","dark":"#AEAEB2"}}]},
  {"type":"text","text":"Nobody is on call","weight":"medium","if":"!primary.name",
   "color":{"light":"#C93400","dark":"#FF9F0A"}},
  {"type":"divider"},
  {"type":"text","text":"Then","style":"caption","weight":"semibold",
   "color":{"light":"#6C6C70","dark":"#AEAEB2"}},
  {"type":"foreach","each":"escalation","max":4,"spacing":6,"body":
    {"type":"hstack","spacing":6,"children":[
      {"type":"text","text":"{{item.name}}","style":"footnote","weight":"medium"},
      {"type":"text","text":"away","style":"caption","if":"!item.available",
       "color":{"light":"#6C6C70","dark":"#AEAEB2"}},
      {"type":"spacer"},
      {"type":"badge","text":"{{item.channel}}","if":"item.channel"}]}}]}`,
		Data: `{
  "title": "Payments API is down",
  "severity": "SEV1",
  "primary": {"name": "Ada Okafor", "phone": "+49 30 1234 5678", "until": "Mon 08:00"},
  "escalation": [
    {"name": "Ben Ruiz", "channel": "SMS", "available": true},
    {"name": "Chi Tan", "channel": "Call"},
    {"name": "Dara Vos", "channel": "Push"}
  ]
}`,
	},
}
