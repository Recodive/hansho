package api

import (
	"embed"
	"html/template"
	"net/http"
)

// The page's fonts and icons are served by the hub itself (the page makes no external request). Big Shoulders Display
// ExtraBold and Atkinson Hyperlegible Regular come from the Hansho site, both under the SIL OFL; the icons are the
// Hansho bell.
//
//go:embed home_assets
var homeAssets embed.FS

// homeAsset serves one embedded file. The names are fixed at registration, nothing from the request reaches the file
// system.
func homeAsset(file, contentType string) http.HandlerFunc {
	data, err := homeAssets.ReadFile("home_assets/" + file)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", contentType)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "public, max-age=604800")
		_, _ = w.Write(data)
	}
}

// groups splits the fingerprint into blocks of eight characters. The blocks are spans without text between them, so
// copying the fingerprint still yields the plain string.
func groups(s string) []string {
	var out []string
	for len(s) > 8 {
		out = append(out, s[:8])
		s = s[8:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// homeTmpl is the page the hub shows at its root address. The hub is an API with no web interface, so this only says
// what the address is, who runs it and where to read more. Everything it shows is already public through
// /device/v1/hub, and html/template escapes the sender-chosen display name.
var homeTmpl = template.Must(template.New("home").Funcs(template.FuncMap{"groups": groups}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<meta name="color-scheme" content="dark light">
<meta name="description" content="{{.Name}} sends notifications to the Hansho app from this Hansho hub.">
<meta name="theme-color" content="#14141A">
<link rel="icon" href="/favicon.ico" sizes="32x32">
<link rel="icon" href="/_hub/favicon.svg" type="image/svg+xml">
<link rel="apple-touch-icon" href="/_hub/apple-touch-icon.png">
<title>{{.Name}} · Hansho hub</title>
<style>
@font-face{font-family:"Big Shoulders Display";font-weight:800;font-display:swap;src:url("/_hub/display.woff2") format("woff2")}
@font-face{font-family:"Atkinson Hyperlegible";font-weight:400;font-display:swap;src:url("/_hub/body.woff2") format("woff2")}
:root{color-scheme:dark light;--bg:#14141a;--card:#1d1d26;--fg:#ede9e1;--dim:#b5b0a6;--line:#34343f;--hi:#e0442a;--ok:#5fbf7f;--bronze:#b88a3e;
--display:"Big Shoulders Display","Arial Narrow",sans-serif;--body:"Atkinson Hyperlegible",system-ui,-apple-system,"Segoe UI",sans-serif;--mono:ui-monospace,"SF Mono",SFMono-Regular,Menlo,Consolas,monospace}
@media (prefers-color-scheme:light){:root{--bg:#f6f3ec;--card:#fffdf8;--fg:#1a1a20;--dim:#575560;--line:#d8d2c5;--hi:#c63319;--ok:#1f7a45;--bronze:#8a6422}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;min-height:100dvh;background:var(--bg);color:var(--fg);font:400 1.0625rem/1.6 var(--body);-webkit-font-smoothing:antialiased;display:flex;flex-direction:column}
::selection{background:var(--hi);color:#14141a}
a{color:var(--fg);text-decoration-color:var(--hi);text-decoration-thickness:2px;text-underline-offset:.2em;transition:color .15s}
a:hover{color:var(--hi)}
a:focus-visible{outline:3px solid var(--hi);outline-offset:3px;border-radius:2px}
main{width:100%;max-width:38rem;margin:0 auto;padding:clamp(2rem,8vw,5rem) max(1.25rem,env(safe-area-inset-left)) 2rem;flex:1}
.brand{display:flex;align-items:center;gap:.75rem;color:var(--hi)}
.mark{width:2.75rem;height:2.75rem;transform-origin:50% 11%}
.brand span{font:800 1.25rem/1 var(--display);letter-spacing:.14em;text-transform:uppercase;color:var(--fg)}
h1{font:800 clamp(2.5rem,12vw,4.5rem)/.95 var(--display);text-transform:uppercase;letter-spacing:.01em;margin:2.5rem 0 1rem;overflow-wrap:anywhere}
.live{display:inline-flex;align-items:center;gap:.5rem;border:2px solid var(--line);padding:.3rem .7rem;font-size:.95rem}
.live i{width:.55rem;height:.55rem;border-radius:50%;background:var(--ok);display:block}
.lead{color:var(--dim);margin:1.5rem 0 2rem;max-width:34rem}
.lead strong{color:var(--fg);font-weight:400}
dl{margin:0;border:2px solid var(--line);background:var(--card)}
dl>div{padding:1rem 1.1rem}
dl>div+div{border-top:2px solid var(--line)}
dt{font:800 .95rem/1 var(--display);letter-spacing:.14em;text-transform:uppercase;color:var(--bronze);margin-bottom:.45rem}
dd{margin:0;overflow-wrap:anywhere}
.fp{font:.95rem/1.9 var(--mono);display:flex;flex-wrap:wrap;gap:0 .9ch}
.fp span{display:inline-block}
.pair{margin-top:2rem;border-left:2px solid var(--hi);padding:.1rem 0 .1rem 1.1rem}
.pair h2{font:800 1.4rem/1 var(--display);letter-spacing:.06em;text-transform:uppercase;margin:0 0 .5rem}
.pair p{margin:0;color:var(--dim)}
footer{padding:1.5rem max(1.25rem,env(safe-area-inset-left));color:var(--dim);font-size:.95rem;text-align:center;border-top:2px solid var(--line)}
@media (prefers-reduced-motion:no-preference){
main>*{animation:rise .45s cubic-bezier(.2,.8,.2,1) backwards}
main>:nth-child(2){animation-delay:.06s}main>:nth-child(3){animation-delay:.12s}main>:nth-child(4){animation-delay:.18s}main>:nth-child(5){animation-delay:.24s}main>:nth-child(6){animation-delay:.3s}
.mark{animation:strike 1.1s cubic-bezier(.3,0,.2,1) .25s both}
.live i{animation:beat 2.4s ease-in-out infinite}
@keyframes rise{from{opacity:0;transform:translateY(10px)}}
@keyframes strike{0%{transform:rotate(0)}20%{transform:rotate(-7deg)}45%{transform:rotate(5deg)}70%{transform:rotate(-2deg)}100%{transform:rotate(0)}}
@keyframes beat{50%{opacity:.35}}
}
</style>
</head>
<body>
<main>
<div class="brand"><svg class="mark" viewBox="0 0 1024 1024" aria-hidden="true" focusable="false"><mask id="m"><rect width="1024" height="1024" fill="white"/><circle cx="335" cy="476.2" r="38.8" fill="black"/><circle cx="453" cy="476.2" r="38.8" fill="black"/><circle cx="571" cy="476.2" r="38.8" fill="black"/><circle cx="689" cy="476.2" r="38.8" fill="black"/><path d="M481.3 106.7 a30.7 30.7 0 0 1 61.4 0 v21.5 h-61.4 Z" fill="black"/></mask><g fill="currentColor" mask="url(#m)"><path d="M435.3 106.7 a76.7 76.7 0 0 1 153.4 0 V185.3 H435.3 Z"/><path d="M228.8 348.1 C228.8 193.9 370.4 155.3 512 155.3 C653.6 155.3 795.2 193.9 795.2 348.1 C801.1 560.2 839.5 762.6 866 832 H158 C184.6 762.6 222.9 560.2 228.8 348.1 Z"/><path d="M158 859 H866 Q880.8 936.2 871.9 994 H152.1 Q143.2 936.2 158 859 Z"/></g></svg><span>Hansho hub</span></div>
<h1>{{.Name}}</h1>
<div><span class="live" role="status"><i></i>Online</span></div>
<p class="lead">This server delivers notifications from <strong>{{.Name}}</strong> to people who have paired their iPhone or Apple Watch with it. It has no web interface: apps and the sender's own backend talk to it directly.</p>
<dl>
<div><dt>Address</dt><dd>{{.Domain}}</dd></div>
<div><dt>Public key fingerprint</dt><dd class="fp">{{range groups .Fingerprint}}<span>{{.}}</span>{{end}}</dd></div>
</dl>
<div class="pair"><h2>Sent here to pair?</h2><p>Open the pairing link or scan the QR code from the sender with the Hansho app. Nothing needs to be done on this page.</p></div>
</main>
<footer>Hansho is made by Recodive. <a href="{{.HomeURL}}">Learn more</a></footer>
</body>
</html>
`))

type homeData struct{ Name, Domain, Fingerprint, HomeURL string }

func (s *server) home(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; font-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("Cache-Control", "no-cache")
	home := s.Cfg.HomeURL
	if home == "" {
		home = "https://hansho.app"
	}
	_ = homeTmpl.Execute(w, homeData{Name: s.Cfg.DisplayName, Domain: s.domain, Fingerprint: s.fp, HomeURL: home})
}
