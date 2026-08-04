import { useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  api,
  attachmentSrc,
  type Attachment,
  type Item,
  type ItemLink,
  type LinkKind,
} from "../api";
import { TYPE_GLYPH } from "../lib";

// How each relationship choice in the add-form maps onto a directed link.
// `outgoing` means the open item is the `from` side.
const RELATIONS: { label: string; kind: LinkKind; outgoing: boolean }[] = [
  { label: "is blocked by", kind: "blocks", outgoing: false },
  { label: "blocks", kind: "blocks", outgoing: true },
  { label: "relates to", kind: "relates_to", outgoing: true },
  { label: "parent of", kind: "parent_of", outgoing: true },
  { label: "child of", kind: "parent_of", outgoing: false },
];

// Phrase a stored link from the perspective of the open item.
function phrase(kind: LinkKind, outgoing: boolean): string {
  if (kind === "blocks") return outgoing ? "blocks" : "blocked by";
  if (kind === "parent_of") return outgoing ? "parent of" : "child of";
  return "relates to";
}

// Screenshots on an item: thumbnail grid + upload (picker or drag-drop) +
// delete. Blobs stream from /api/attachments/{id} (key-authed).
function AttachmentsSection({ item }: { item: Item }) {
  const qc = useQueryClient();
  const fileInput = useRef<HTMLInputElement>(null);
  const [dragOver, setDragOver] = useState(false);

  const attsQ = useQuery({
    queryKey: ["attachments", item.id],
    queryFn: () => api.itemAttachments(item.id),
  });
  const invalidate = () =>
    qc.invalidateQueries({ queryKey: ["attachments", item.id] });
  const upload = useMutation({
    mutationFn: (files: File[]) => api.uploadAttachments(item.id, files),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteAttachment(id),
    onSuccess: invalidate,
  });

  function addFiles(list: FileList | File[]) {
    const files = Array.from(list).filter((f) => f.type.startsWith("image/"));
    if (files.length) upload.mutate(files);
  }

  const atts = attsQ.data ?? [];
  return (
    <div
      onDragOver={(e) => {
        e.preventDefault();
        setDragOver(true);
      }}
      onDragLeave={() => setDragOver(false)}
      onDrop={(e) => {
        e.preventDefault();
        setDragOver(false);
        addFiles(e.dataTransfer.files);
      }}
    >
      <p className="section-title">
        Screenshots
        <button
          className="icon-btn"
          title="Add screenshots"
          style={{ marginLeft: 8 }}
          onClick={() => fileInput.current?.click()}
        >
          ＋
        </button>
      </p>
      <input
        ref={fileInput}
        type="file"
        accept="image/*"
        multiple
        hidden
        onChange={(e) => {
          if (e.target.files) addFiles(e.target.files);
          e.target.value = "";
        }}
      />
      {atts.length === 0 ? (
        <p className="muted sm">
          {dragOver
            ? "Drop images to attach…"
            : upload.isPending
              ? "Uploading…"
              : "None — drop images here or click ＋."}
        </p>
      ) : (
        <div className="att-grid" data-dragover={dragOver || undefined}>
          {atts.map((a: Attachment) => (
            <div key={a.id} className="att-thumb" title={a.filename}>
              <a href={attachmentSrc(a)} target="_blank" rel="noreferrer">
                <img src={attachmentSrc(a)} alt={a.filename} loading="lazy" />
              </a>
              <button
                className="icon-btn att-x"
                title="Delete screenshot"
                onClick={() => remove.mutate(a.id)}
              >
                ✕
              </button>
            </div>
          ))}
          {upload.isPending && <p className="muted sm">Uploading…</p>}
        </div>
      )}
      {upload.isError && (
        <p className="muted sm">
          Upload failed: {(upload.error as Error).message}
        </p>
      )}
    </div>
  );
}

export function ItemLinksModal({
  item,
  onClose,
}: {
  item: Item;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [relIdx, setRelIdx] = useState(0);
  const [targetQ, setTargetQ] = useState("");
  const [target, setTarget] = useState<Item | null>(null);

  const linksQ = useQuery({
    queryKey: ["links", item.id],
    queryFn: () => api.itemLinks(item.id),
  });
  // Unfiltered item list so link targets and titles resolve even when the
  // board is filtered down.
  const allQ = useQuery({
    queryKey: ["items", "all-for-links"],
    queryFn: () => api.items({}),
  });
  const byId = useMemo(() => {
    const m = new Map<string, Item>();
    for (const it of allQ.data ?? []) m.set(it.id, it);
    return m;
  }, [allQ.data]);

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["links", item.id] });
    qc.invalidateQueries({ queryKey: ["items"] });
  };
  const add = useMutation({
    mutationFn: (body: { from: string; to: string; kind: LinkKind }) =>
      api.createLink(body),
    onSuccess: () => {
      setTarget(null);
      setTargetQ("");
      invalidate();
    },
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteLink(id),
    onSuccess: invalidate,
  });

  const candidates = useMemo(() => {
    const q = targetQ.trim().toLowerCase();
    if (!q) return [];
    return (allQ.data ?? [])
      .filter(
        (it) =>
          it.id !== item.id &&
          (it.ref.toLowerCase().includes(q) ||
            it.title.toLowerCase().includes(q)),
      )
      .slice(0, 8);
  }, [allQ.data, targetQ, item.id]);

  function submit() {
    if (!target) return;
    const rel = RELATIONS[relIdx];
    add.mutate({
      from: rel.outgoing ? item.id : target.id,
      to: rel.outgoing ? target.id : item.id,
      kind: rel.kind,
    });
  }

  return (
    <div className="drawer-backdrop centered" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="drawer-head">
          <span className="glyph">{TYPE_GLYPH[item.type] ?? "◆"}</span>
          <h2 className="ellipsis" title={item.title}>
            {item.title}
          </h2>
          <span className="card-ref">{item.ref}</span>
          <button className="icon-btn" onClick={onClose} title="Close">
            ✕
          </button>
        </div>

        <AttachmentsSection item={item} />

        <div>
          <p className="section-title">Links</p>
          {linksQ.isLoading ? (
            <p className="muted sm">Loading…</p>
          ) : (linksQ.data ?? []).length === 0 ? (
            <p className="muted sm">No links yet.</p>
          ) : (
            <ul className="drawer-list">
              {(linksQ.data ?? []).map((l: ItemLink) => {
                const outgoing = l.from_item_id === item.id;
                const otherId = outgoing ? l.to_item_id : l.from_item_id;
                const other = byId.get(otherId);
                const isBlockingMe =
                  !outgoing &&
                  l.kind === "blocks" &&
                  other &&
                  other.status !== "done" &&
                  other.status !== "wontfix";
                return (
                  <li key={l.id}>
                    <span className={`link-phrase${isBlockingMe ? " blocking" : ""}`}>
                      {phrase(l.kind, outgoing)}
                    </span>
                    <span className="ellipsis link-target">
                      {other ? (
                        <>
                          <span className="card-ref">{other.ref}</span>{" "}
                          {other.title}
                          {(other.status === "done" ||
                            other.status === "wontfix") && (
                            <span className="muted sm"> ({other.status})</span>
                          )}
                        </>
                      ) : (
                        <span className="mono">{otherId.slice(0, 8)}…</span>
                      )}
                    </span>
                    <button
                      className="icon-btn"
                      title="Remove link"
                      onClick={() => remove.mutate(l.id)}
                    >
                      ✕
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </div>

        <div>
          <p className="section-title">Add link</p>
          <div className="link-form">
            <select
              value={relIdx}
              onChange={(e) => setRelIdx(Number(e.target.value))}
            >
              {RELATIONS.map((r, i) => (
                <option key={r.label} value={i}>
                  {r.label}
                </option>
              ))}
            </select>
            {target ? (
              <button
                className="btn target-pick"
                title="Change target"
                onClick={() => setTarget(null)}
              >
                <span className="card-ref">{target.ref}</span> {target.title}
              </button>
            ) : (
              <input
                placeholder="Find item by ref or title…"
                value={targetQ}
                autoFocus
                onChange={(e) => setTargetQ(e.target.value)}
              />
            )}
            <button
              className="btn primary"
              disabled={!target || add.isPending}
              onClick={submit}
            >
              Add
            </button>
          </div>
          {!target && candidates.length > 0 && (
            <ul className="drawer-list link-candidates">
              {candidates.map((c) => (
                <li
                  key={c.id}
                  className="clickable"
                  onClick={() => setTarget(c)}
                >
                  <span className="card-ref">{c.ref}</span>
                  <span className="ellipsis">{c.title}</span>
                  <span className="muted sm">{c.status}</span>
                </li>
              ))}
            </ul>
          )}
          {add.isError && (
            <p className="muted sm">Failed: {(add.error as Error).message}</p>
          )}
        </div>
      </div>
    </div>
  );
}
