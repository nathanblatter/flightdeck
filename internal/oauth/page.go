package oauth

// authorizeHTML is the consent/login page. Self-contained: no assets, no
// scripts, works on a phone. The credential is an existing API key, which
// keeps a single identity model (keys) for every way into the instance.
const authorizeHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Title}} · flightdeck</title>
<style>
  :root { --bg:#f6f7f9; --card:#fff; --fg:#111827; --muted:#6b7280; --line:#e5e7eb; --accent:#2563eb; --err:#b91c1c; --errbg:#fef2f2; }
  @media (prefers-color-scheme: dark) {
    :root { --bg:#0b0f17; --card:#131a26; --fg:#e5e7eb; --muted:#9ca3af; --line:#273244; --accent:#60a5fa; --err:#fca5a5; --errbg:#2a1215; }
  }
  * { box-sizing:border-box }
  body { margin:0; background:var(--bg); color:var(--fg); font:16px/1.5 system-ui,-apple-system,Segoe UI,sans-serif; display:flex; min-height:100vh; align-items:center; justify-content:center; padding:16px }
  .card { background:var(--card); border:1px solid var(--line); border-radius:12px; padding:28px; width:100%; max-width:420px }
  h1 { font-size:20px; margin:0 0 6px }
  p { margin:0 0 16px; color:var(--muted) }
  b { color:var(--fg) }
  label { display:block; font-size:14px; margin-bottom:6px }
  input[type=password] { width:100%; font:inherit; padding:10px 12px; border:1px solid var(--line); border-radius:8px; background:transparent; color:var(--fg) }
  button { margin-top:16px; width:100%; font:inherit; font-weight:600; padding:11px; border:0; border-radius:8px; background:var(--accent); color:#fff; cursor:pointer }
  .err { background:var(--errbg); color:var(--err); border-radius:8px; padding:10px 12px; margin-bottom:16px; font-size:14px }
  .fine { font-size:13px; margin-top:14px; margin-bottom:0 }
</style>
</head>
<body>
<main class="card">
  <h1>{{.Title}}</h1>
  {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
  {{if .Client}}
  <p><b>{{.Client}}</b> wants to use this flightdeck instance as you. It will act with the scopes of the key you sign in with.</p>
  <form method="post" action="/oauth/authorize" autocomplete="off">
    <input type="hidden" name="client_id" value="{{.Params.ClientID}}">
    <input type="hidden" name="redirect_uri" value="{{.Params.RedirectURI}}">
    <input type="hidden" name="response_type" value="{{.Params.ResponseType}}">
    <input type="hidden" name="state" value="{{.Params.State}}">
    <input type="hidden" name="scope" value="{{.Params.Scope}}">
    <input type="hidden" name="code_challenge" value="{{.Params.Challenge}}">
    <input type="hidden" name="code_challenge_method" value="{{.Params.Method}}">
    <input type="hidden" name="resource" value="{{.Params.Resource}}">
    <label for="api_key">flightdeck API key</label>
    <input id="api_key" name="api_key" type="password" required autofocus inputmode="text" autocapitalize="off" spellcheck="false" placeholder="fd_…">
    <button type="submit">Allow access</button>
  </form>
  <p class="fine">Not expecting this? Close the page. Nothing is granted until you submit a key.</p>
  {{end}}
</main>
</body>
</html>`
