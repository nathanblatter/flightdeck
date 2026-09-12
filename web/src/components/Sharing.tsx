import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Share } from "../api";
import { relativeTime } from "../lib";

/** Live status for one share, so a link that quietly stopped working is visible. */
function ShareRow({ share, onRemove }: { share: Share; onRemove: () => void }) {
  const last = share.last_recv_at ?? share.last_send_at;
  return (
    <li className="col">
      <div className="feed-line">
        <strong>{share.peer_name || "peer"}</strong>
        {share.last_error ? (
          <span className="sm error">sync failing</span>
        ) : share.enabled ? (
          <span className="sm muted">
            {last ? `synced ${relativeTime(last)}` : "waiting for first sync"}
          </span>
        ) : (
          <span className="sm muted">disabled</span>
        )}
        <span className="spacer" />
        <button className="btn ghost sm" onClick={onRemove}>
          Unshare
        </button>
      </div>
      {share.last_error && <div className="sm error">{share.last_error}</div>}
    </li>
  );
}

/**
 * Sharing controls for one project, shown inside the project drawer.
 *
 * A share is bidirectional: both instances can write, and changes flow both
 * ways. Unsharing stops the exchange but keeps everything that already arrived.
 */
export function ProjectSharing({ slug, projectId }: { slug: string; projectId: string }) {
  const qc = useQueryClient();
  const [peerName, setPeerName] = useState("");
  const [invite, setInvite] = useState("");
  const [copied, setCopied] = useState(false);

  const config = useQuery({ queryKey: ["shareConfig"], queryFn: api.shareConfig });
  const shares = useQuery({ queryKey: ["shares"], queryFn: api.shares });
  const mine = (shares.data ?? []).filter((s) => s.project_id === projectId);

  const share = useMutation({
    mutationFn: () => api.shareProject({ project: slug, peer_name: peerName.trim() || undefined }),
    onSuccess: (res) => {
      setInvite(res.invite);
      setPeerName("");
      qc.invalidateQueries({ queryKey: ["shares"] });
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => api.deleteShare(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["shares"] }),
  });

  if (!config.data?.configured) {
    return (
      <section>
        <h3 className="section-title">Sharing</h3>
        <p className="muted sm">
          This instance isn’t set up to share yet. Add a mailbox server and its
          client certificate in Settings first.
        </p>
      </section>
    );
  }

  return (
    <section>
      <h3 className="section-title">Sharing</h3>

      {mine.length > 0 && (
        <ul className="drawer-list">
          {mine.map((s) => (
            <ShareRow key={s.id} share={s} onRemove={() => remove.mutate(s.id)} />
          ))}
        </ul>
      )}

      {config.data.can_create ? (
        <>
          <p className="muted sm">
            Share this project with another flightdeck instance. Both sides can
            read and write, and changes sync every few seconds.
          </p>
          <div className="quick-add" style={{ padding: 0, background: "none", border: 0 }}>
            <input
              placeholder="Name the other instance (e.g. work-laptop)"
              value={peerName}
              onChange={(e) => setPeerName(e.target.value)}
            />
            <button
              className="btn primary"
              disabled={share.isPending}
              onClick={() => share.mutate()}
            >
              {share.isPending ? "Creating…" : "Create invite"}
            </button>
          </div>
        </>
      ) : (
        <p className="muted sm">
          Creating an invite needs the mailbox admin token. This instance can
          join shares but not start them.
        </p>
      )}

      {share.isError && <p className="sm error">{(share.error as Error).message}</p>}

      {invite && (
        <div className="invite-box">
          <p className="sm">
            <strong>Paste this into the other instance.</strong> It contains the
            encryption key for this project — send it somewhere you trust, and
            treat it like a password. It is shown only once.
          </p>
          <textarea readOnly value={invite} rows={4} onFocus={(e) => e.currentTarget.select()} />
          <div className="drawer-actions">
            <button
              className="btn"
              onClick={() => {
                navigator.clipboard?.writeText(invite);
                setCopied(true);
              }}
            >
              {copied ? "Copied" : "Copy invite"}
            </button>
            <button className="btn ghost" onClick={() => { setInvite(""); setCopied(false); }}>
              Done
            </button>
          </div>
        </div>
      )}
    </section>
  );
}

/** Join a project shared from another instance by pasting its invite code. */
export function AcceptInvite({ onDone }: { onDone: () => void }) {
  const qc = useQueryClient();
  const [code, setCode] = useState("");
  const [slug, setSlug] = useState("");

  const accept = useMutation({
    mutationFn: () => api.acceptInvite({ invite: code.trim(), slug: slug.trim() || undefined }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["projects"] });
      qc.invalidateQueries({ queryKey: ["shares"] });
      qc.invalidateQueries({ queryKey: ["items"] });
      onDone();
    },
  });

  return (
    <div className="quick-add col">
      <p className="muted sm">
        Paste an invite from another flightdeck instance. The project appears
        here and stays in sync both ways.
      </p>
      <textarea
        placeholder="fdshare_…"
        rows={3}
        value={code}
        onChange={(e) => setCode(e.target.value)}
      />
      <div className="feed-line">
        <input
          placeholder="Local name (optional — defaults to theirs)"
          value={slug}
          onChange={(e) => setSlug(e.target.value)}
        />
        <button
          className="btn primary"
          disabled={!code.trim() || accept.isPending}
          onClick={() => accept.mutate()}
        >
          {accept.isPending ? "Joining…" : "Join shared project"}
        </button>
        <button className="btn ghost" onClick={onDone}>
          Cancel
        </button>
      </div>
      {accept.isError && <p className="sm error">{(accept.error as Error).message}</p>}
    </div>
  );
}
