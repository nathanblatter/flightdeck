import { useState } from "react";
import { completeSetup, setApiKey, type MintedKey } from "../api";

// First-run setup wizard for a fresh instance (see internal/api/setup.go).
// Collects everything in local state and submits once at the mint step, then
// shows the raw keys (once) plus ready-to-paste integration snippets: the
// Claude Code .mcp.json config and the Apple Shortcuts capture recipe that
// stands in for a desktop widget (WidgetKit widgets can't take text input).

type Step = "token" | "identity" | "keys";

const KEY_PRESETS = [
  { name: "personal", scopes: ["read", "write"], hint: "you — the web UI stores this one" },
  { name: "agent", scopes: ["read", "write"], hint: "Claude Code / MCP clients" },
  { name: "ingest", scopes: ["ingest"], hint: "capture Shortcut & bug widget (safe to embed)" },
];

export function SetupWizard({ onDone }: { onDone: () => void }) {
  const [step, setStep] = useState<Step>("token");
  const [token, setToken] = useState("");
  const [name, setName] = useState("");
  const [openaiKey, setOpenaiKey] = useState("");
  const [usageAnalytics, setUsageAnalytics] = useState(true);
  const [bugWidget, setBugWidget] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [minted, setMinted] = useState<MintedKey[] | null>(null);

  async function submit() {
    setBusy(true);
    setError("");
    try {
      const res = await completeSetup(token.trim(), {
        instance_name: name.trim(),
        ...(openaiKey.trim() ? { openai_api_key: openaiKey.trim() } : {}),
        flags: { usage_analytics: usageAnalytics, bug_widget: bugWidget },
        keys: KEY_PRESETS.map(({ name, scopes }) => ({ name, scopes })),
      });
      const personal = res.keys.find((k) => k.name === "personal");
      if (personal) setApiKey(personal.key);
      setMinted(res.keys);
      setStep("keys");
    } catch (e) {
      setError(e instanceof Error ? e.message : "setup failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="gate">
      <div className="gate-card wizard">
        <h1>🛩 Flightdeck setup</h1>

        {step === "token" && (
          <>
            <p className="muted">
              First run on this instance. Paste the setup token printed by{" "}
              <code>flightdeck up</code> (it's also in the instance{" "}
              <code>.env</code> as <code>FLIGHTDECK_SETUP_TOKEN</code>).
            </p>
            <input
              type="password"
              placeholder="fdsetup_…"
              value={token}
              onChange={(e) => setToken(e.target.value)}
            />
            <button
              className="btn primary"
              disabled={!token.trim()}
              onClick={() => setStep("identity")}
            >
              Continue
            </button>
          </>
        )}

        {step === "identity" && (
          <>
            <label className="wizard-label">
              Instance name
              <input
                placeholder='e.g. "Flightdeck @ Lumion"'
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <label className="wizard-label">
              OpenAI API key <span className="muted sm">(optional)</span>
              <input
                type="password"
                placeholder="sk-… — enables semantic search"
                value={openaiKey}
                onChange={(e) => setOpenaiKey(e.target.value)}
              />
              <span className="muted sm">
                Without a key, search falls back to full-text — everything else
                works. You can add one later via PUT /api/settings.
              </span>
            </label>
            <label className="wizard-check">
              <input
                type="checkbox"
                checked={usageAnalytics}
                onChange={(e) => setUsageAnalytics(e.target.checked)}
              />
              Record usage analytics (per-tool-call telemetry)
            </label>
            <label className="wizard-check">
              <input
                type="checkbox"
                checked={bugWidget}
                onChange={(e) => setBugWidget(e.target.checked)}
              />
              Serve the embeddable bug widget (/bug-widget.js)
            </label>
            <p className="muted sm">
              Finishing creates three API keys: {KEY_PRESETS.map((k) => k.name).join(", ")}.
            </p>
            {error && <p className="wizard-error">{error}</p>}
            <button
              className="btn primary"
              disabled={!name.trim() || busy}
              onClick={submit}
            >
              {busy ? "Setting up…" : "Finish setup"}
            </button>
          </>
        )}

        {step === "keys" && minted && (
          <>
            <p className="muted">
              Keys created — <strong>shown once</strong>, store them now. The{" "}
              <em>personal</em> key is already saved in this browser.
            </p>
            {minted.map((k) => (
              <div key={k.name} className="wizard-key">
                <div className="wizard-key-head">
                  <strong>{k.name}</strong>{" "}
                  <span className="muted sm">
                    [{k.scopes.join(", ")}] — {KEY_PRESETS.find((p) => p.name === k.name)?.hint}
                  </span>
                </div>
                <div className="wizard-key-row">
                  <code>{k.key}</code>
                  <button className="btn" onClick={() => navigator.clipboard.writeText(k.key)}>
                    copy
                  </button>
                </div>
              </div>
            ))}
            <Integrations minted={minted} />
            <button className="btn primary" onClick={onDone}>
              Open the board
            </button>
          </>
        )}
      </div>
    </div>
  );
}

function Snippet({ title, text }: { title: string; text: string }) {
  return (
    <details className="wizard-snippet">
      <summary>
        {title}{" "}
        <button
          className="btn"
          onClick={(e) => {
            e.preventDefault();
            navigator.clipboard.writeText(text);
          }}
        >
          copy
        </button>
      </summary>
      <pre>
        <code>{text}</code>
      </pre>
    </details>
  );
}

function Integrations({ minted }: { minted: MintedKey[] }) {
  const base = window.location.origin;
  const agent = minted.find((k) => k.name === "agent")?.key ?? "<agent key>";
  const ingest = minted.find((k) => k.name === "ingest")?.key ?? "<ingest key>";

  const mcpJson = JSON.stringify(
    {
      mcpServers: {
        flightdeck: {
          type: "http",
          url: `${base}/mcp`,
          headers: { "X-API-Key": agent },
        },
      },
    },
    null,
    2,
  );

  const curl = `curl -X POST ${base}/api/ingest/capture \\
  -H "X-API-Key: ${ingest}" -H "Content-Type: application/json" \\
  -d '{"project":"<slug>","title":"do the thing","type":"task"}'`;

  const shortcut = `Apple Shortcuts capture (a desktop-widget quick-add):
Open Shortcuts.app → + New Shortcut, add these actions in order:

1. Ask for Input        · Text · prompt "Capture what?"
2. Get Contents of URL  · GET ${base}/api/ingest/projects
                        · Headers: X-API-Key = ${ingest}
3. Choose from List     · input: Contents of URL
4. List                 · items: task, bug, idea
5. Choose from List     · input: List
6. Get Contents of URL  · POST ${base}/api/ingest/capture
                        · Headers: X-API-Key = ${ingest}
                        · Request Body (JSON):
                            project = Chosen Item (from action 3)
                            type    = Chosen Item (from action 5)
                            title   = Provided Input (from action 1)
7. Show Notification    · "Captured ✓"

Send exactly the fields above — unknown JSON fields are rejected.
Then: right-click your desktop → Edit Widgets → add the Shortcuts
widget and pick this Shortcut. One tap runs the capture flow.`;

  return (
    <>
      <h2 className="wizard-h2">Hook things up</h2>
      <Snippet title=".mcp.json for Claude Code (agent key)" text={mcpJson} />
      <Snippet title="Quick-capture curl (ingest key)" text={curl} />
      <Snippet title="Apple Shortcut — meeting quick-capture" text={shortcut} />
    </>
  );
}
