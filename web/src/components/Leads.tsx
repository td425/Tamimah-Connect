import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  can,
  createLead,
  createLeadList,
  deleteLead,
  deleteLeadList,
  getLeadList,
  importLeads,
  listLeadLists,
  resetLeadList,
  searchLeads,
  setLeadStatus,
  updateLead,
  updateLeadList,
  type CustomField,
  type DupScope,
  type Lead,
  type LeadList,
  type Me,
} from "../api";
import type { Notify } from "../types";

const PAGE = 50;

// The statuses the system itself sets. A lead may carry any status — campaigns
// bring their own dispositions in phase 2 — so this drives the pickers only.
const STATUSES = ["NEW", "CALLBK", "SALE", "NI", "NA", "BUSY", "DNC", "DROP"];

const BLANK_LIST: LeadList = {
  id: 0,
  name: "",
  description: "",
  campaignId: "",
  active: true,
  expiresOn: "",
  customFields: [],
  leadCount: 0,
};

function blankLead(listId: number): Lead {
  return {
    id: 0,
    listId,
    status: "NEW",
    calledCount: 0,
    phoneCode: "",
    phoneNumber: "",
    altPhone: "",
    altPhoneTwo: "",
    title: "",
    firstName: "",
    lastName: "",
    email: "",
    address1: "",
    address2: "",
    city: "",
    state: "",
    postalCode: "",
    country: "",
    comments: "",
    vendorLeadCode: "",
    sourceId: "",
    owner: "",
    custom: {},
  };
}

export default function Leads({ notify, me }: { notify: Notify; me: Me }) {
  const canCreate = can(me, "leads", "create");
  const canEdit = can(me, "leads", "edit");
  const canDelete = can(me, "leads", "delete");

  const [lists, setLists] = useState<LeadList[]>([]);
  const [listsLoading, setListsLoading] = useState(true);
  const [editingList, setEditingList] = useState<LeadList | null>(null);
  const [importInto, setImportInto] = useState<LeadList | null>(null);

  // Lead browser state.
  const [listFilter, setListFilter] = useState(0); // 0 = every list
  const [status, setStatus] = useState("");
  const [phone, setPhone] = useState("");
  const [name, setName] = useState("");
  const [offset, setOffset] = useState(0);
  const [leads, setLeads] = useState<Lead[]>([]);
  const [total, setTotal] = useState(0);
  const [leadsLoading, setLeadsLoading] = useState(true);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [editingLead, setEditingLead] = useState<Lead | null>(null);

  const refreshLists = useCallback(() => {
    setListsLoading(true);
    listLeadLists()
      .then(setLists)
      .catch((e) => notify({ kind: "err", text: (e as Error).message }))
      .finally(() => setListsLoading(false));
  }, [notify]);

  useEffect(refreshLists, [refreshLists]);

  // Debounce the free-text filters so typing doesn't fire a query per keystroke.
  const [debounced, setDebounced] = useState({ phone: "", name: "" });
  useEffect(() => {
    const t = setTimeout(() => setDebounced({ phone, name }), 300);
    return () => clearTimeout(t);
  }, [phone, name]);

  const refreshLeads = useCallback(() => {
    setLeadsLoading(true);
    searchLeads({
      listId: listFilter || undefined,
      status: status || undefined,
      phone: debounced.phone || undefined,
      name: debounced.name || undefined,
      limit: PAGE,
      offset,
    })
      .then((p) => {
        setLeads(p.leads);
        setTotal(p.total);
      })
      .catch((e) => notify({ kind: "err", text: (e as Error).message }))
      .finally(() => setLeadsLoading(false));
  }, [listFilter, status, debounced, offset, notify]);

  useEffect(refreshLeads, [refreshLeads]);

  // Any filter change invalidates both the page and the selection.
  useEffect(() => {
    setOffset(0);
    setSelected(new Set());
  }, [listFilter, status, debounced]);

  const listById = useMemo(() => {
    const m = new Map<number, LeadList>();
    for (const l of lists) m.set(l.id, l);
    return m;
  }, [lists]);

  const onDeleteList = async (l: LeadList) => {
    const warning =
      l.leadCount > 0
        ? `Delete list "${l.name}" AND its ${l.leadCount.toLocaleString()} lead(s)? This cannot be undone.`
        : `Delete list "${l.name}"?`;
    if (!confirm(warning)) return;
    try {
      await deleteLeadList(l.id, l.leadCount);
      notify({ kind: "ok", text: `Deleted list ${l.name}` });
      if (listFilter === l.id) setListFilter(0);
      refreshLists();
      refreshLeads();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  const onResetList = async (l: LeadList) => {
    if (
      !confirm(
        `Reset every lead in "${l.name}" back to NEW and clear its call counts?\n\nThis re-arms ${l.leadCount.toLocaleString()} lead(s) for dialing.`
      )
    )
      return;
    try {
      const res = await resetLeadList(l.id);
      notify({ kind: "ok", text: `Reset ${res.leads.toLocaleString()} lead(s) in ${l.name}` });
      refreshLists();
      refreshLeads();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  const onDeleteLead = async (d: Lead) => {
    if (!confirm(`Delete lead ${d.phoneNumber}${d.lastName ? ` (${d.firstName} ${d.lastName})` : ""}?`)) return;
    try {
      await deleteLead(d.id);
      notify({ kind: "ok", text: "Lead deleted" });
      refreshLists();
      refreshLeads();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  const onBulkStatus = async (next: string) => {
    const ids = [...selected];
    if (ids.length === 0 || !next) return;
    if (!confirm(`Set ${ids.length} lead(s) to ${next}?`)) return;
    try {
      const res = await setLeadStatus(ids, next);
      notify({ kind: "ok", text: `Updated ${res.updated} lead(s)` });
      setSelected(new Set());
      refreshLists();
      refreshLeads();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  const toggleOne = (id: number) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const allOnPage = leads.length > 0 && leads.every((d) => selected.has(d.id));
  const toggleAll = () =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (allOnPage) leads.forEach((d) => next.delete(d.id));
      else leads.forEach((d) => next.add(d.id));
      return next;
    });

  const from = total === 0 ? 0 : offset + 1;
  const to = Math.min(offset + PAGE, total);
  const activeList = listById.get(listFilter) ?? null;

  return (
    <>
      <div className="page-head">
        <h2>Leads</h2>
        {canCreate && (
          <span className="row-action">
            <button className="btn ghost" onClick={() => setImportInto(activeList ?? lists[0] ?? null)} disabled={lists.length === 0}>
              ↑ Import CSV
            </button>
            <button
              className="btn ghost"
              onClick={() => setEditingLead(blankLead(listFilter || lists[0]?.id || 0))}
              disabled={lists.length === 0}
            >
              + New Lead
            </button>
            <button className="btn" onClick={() => setEditingList({ ...BLANK_LIST })}>
              + New List
            </button>
          </span>
        )}
      </div>

      <section className="panel">
        <header>Lists</header>
        {listsLoading ? (
          <div className="empty">Loading…</div>
        ) : lists.length === 0 ? (
          <div className="empty">
            No lists yet. A list is a batch of leads loaded for one purpose — create one, then
            import a CSV of numbers into it.
          </div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Campaign</th>
                <th>Leads</th>
                <th>Status</th>
                <th>Expires</th>
                <th>Custom fields</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {lists.map((l) => (
                <tr key={l.id}>
                  <td>
                    <a
                      href="#leads"
                      onClick={(e) => {
                        e.preventDefault();
                        setListFilter(l.id);
                      }}
                      title="Show only this list's leads"
                    >
                      {l.name}
                    </a>
                    {l.description && <div className="dash-sub">{l.description}</div>}
                  </td>
                  <td>{l.campaignId || <span className="hint-inline">unassigned</span>}</td>
                  <td>{l.leadCount.toLocaleString()}</td>
                  <td>
                    <span className={`badge ${l.active ? "" : "offline"}`}>
                      {l.active ? "active" : "inactive"}
                    </span>
                  </td>
                  <td>{l.expiresOn || "—"}</td>
                  <td>{l.customFields.length || "—"}</td>
                  <td className="row-action">
                    {canCreate && (
                      <button className="btn small" onClick={() => setImportInto(l)}>
                        Import
                      </button>
                    )}
                    {canEdit && (
                      <>
                        <button className="btn small" onClick={() => openList(l.id, setEditingList, notify)}>
                          Edit
                        </button>
                        <button className="btn small warn" onClick={() => onResetList(l)}>
                          Reset
                        </button>
                      </>
                    )}
                    {canDelete && (
                      <button className="btn danger" onClick={() => onDeleteList(l)}>
                        Delete
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="panel">
        <header>
          Leads{activeList ? ` · ${activeList.name}` : ""}
        </header>

        <div className="form" style={{ paddingBottom: 0 }}>
          <div className="form-row">
            <label>
              List
              <select value={listFilter} onChange={(e) => setListFilter(parseInt(e.target.value, 10))}>
                <option value={0}>All lists</option>
                {lists.map((l) => (
                  <option key={l.id} value={l.id}>
                    {l.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Status
              <select value={status} onChange={(e) => setStatus(e.target.value)}>
                <option value="">Any</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Phone number
              <input value={phone} placeholder="any part of the number" onChange={(e) => setPhone(e.target.value)} />
            </label>
            <label>
              Name
              <input value={name} placeholder="first or last name" onChange={(e) => setName(e.target.value)} />
            </label>
          </div>
        </div>

        {selected.size > 0 && canEdit && (
          <div className="form" style={{ paddingTop: 0 }}>
            <div className="row-action">
              <span className="hint-inline">{selected.size} selected —</span>
              <select
                defaultValue=""
                onChange={(e) => {
                  onBulkStatus(e.target.value);
                  e.target.value = "";
                }}
              >
                <option value="">Set status to…</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </select>
              <button className="btn ghost small" onClick={() => setSelected(new Set())}>
                Clear selection
              </button>
            </div>
          </div>
        )}

        {leadsLoading ? (
          <div className="empty">Loading…</div>
        ) : leads.length === 0 ? (
          <div className="empty">
            {total === 0 && !phone && !name && !status
              ? "No leads yet. Import a CSV into a list to get started."
              : "No leads match these filters."}
          </div>
        ) : (
          <table>
            <thead>
              <tr>
                {canEdit && (
                  <th style={{ width: 28 }}>
                    <input type="checkbox" checked={allOnPage} onChange={toggleAll} title="Select page" />
                  </th>
                )}
                <th>Phone</th>
                <th>Name</th>
                <th>List</th>
                <th>Status</th>
                <th>Called</th>
                <th>Last call</th>
                <th>Owner</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {leads.map((d) => (
                <tr key={d.id}>
                  {canEdit && (
                    <td>
                      <input type="checkbox" checked={selected.has(d.id)} onChange={() => toggleOne(d.id)} />
                    </td>
                  )}
                  <td className="mono-sm">
                    {d.phoneCode && <span className="hint-inline">+{d.phoneCode} </span>}
                    {d.phoneNumber}
                  </td>
                  <td>{[d.firstName, d.lastName].filter(Boolean).join(" ") || "—"}</td>
                  <td>{d.listName || d.listId}</td>
                  <td>
                    <span className={`badge ${d.status === "NEW" ? "" : "offline"}`}>{d.status}</span>
                  </td>
                  <td>{d.calledCount}</td>
                  <td>{d.lastCalledAt ? new Date(d.lastCalledAt).toLocaleString() : "—"}</td>
                  <td>{d.owner || "—"}</td>
                  <td className="row-action">
                    {canEdit && (
                      <button className="btn small" onClick={() => setEditingLead(d)}>
                        Edit
                      </button>
                    )}
                    {canDelete && (
                      <button className="btn danger" onClick={() => onDeleteLead(d)}>
                        Delete
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        <div className="pager">
          <span>
            {from.toLocaleString()}–{to.toLocaleString()} of {total.toLocaleString()}
          </span>
          <span>
            <button
              className="btn ghost small"
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - PAGE))}
            >
              ‹ Prev
            </button>
            <button className="btn ghost small" disabled={to >= total} onClick={() => setOffset(offset + PAGE)}>
              Next ›
            </button>
          </span>
        </div>
      </section>

      {editingList && (
        <ListForm
          initial={editingList}
          onClose={() => setEditingList(null)}
          onSaved={(msg) => {
            notify({ kind: "ok", text: msg });
            setEditingList(null);
            refreshLists();
          }}
          onError={(msg) => notify({ kind: "err", text: msg })}
        />
      )}

      {editingLead && (
        <LeadForm
          initial={editingLead}
          lists={lists}
          onClose={() => setEditingLead(null)}
          onSaved={(msg) => {
            notify({ kind: "ok", text: msg });
            setEditingLead(null);
            refreshLists();
            refreshLeads();
          }}
          onError={(msg) => notify({ kind: "err", text: msg })}
        />
      )}

      {importInto && (
        <ImportModal
          lists={lists}
          initialList={importInto}
          onClose={() => setImportInto(null)}
          onDone={(msg) => {
            notify({ kind: "ok", text: msg });
            refreshLists();
            refreshLeads();
          }}
          onError={(msg) => notify({ kind: "err", text: msg })}
        />
      )}
    </>
  );
}

// openList fetches the full list (including its status breakdown) before
// opening the editor, so the form always edits current server state.
async function openList(id: number, set: (l: LeadList) => void, notify: Notify) {
  try {
    set(await getLeadList(id));
  } catch (e) {
    notify({ kind: "err", text: (e as Error).message });
  }
}

// --- List editor -------------------------------------------------------------

function ListForm({
  initial,
  onClose,
  onSaved,
  onError,
}: {
  initial: LeadList;
  onClose: () => void;
  onSaved: (msg: string) => void;
  onError: (msg: string) => void;
}) {
  const [f, setF] = useState<LeadList>(initial);
  const [busy, setBusy] = useState(false);
  const isNew = initial.id === 0;
  const set = <K extends keyof LeadList>(k: K, v: LeadList[K]) => setF((prev) => ({ ...prev, [k]: v }));

  const setField = (i: number, patch: Partial<CustomField>) =>
    setF((prev) => ({
      ...prev,
      customFields: prev.customFields.map((c, j) => (j === i ? { ...c, ...patch } : c)),
    }));

  const addField = () =>
    setF((prev) => ({
      ...prev,
      customFields: [...prev.customFields, { name: "", label: "", type: "text" }],
    }));

  const removeField = (i: number) =>
    setF((prev) => ({ ...prev, customFields: prev.customFields.filter((_, j) => j !== i) }));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      if (isNew) {
        await createLeadList(f);
        onSaved(`Created list ${f.name}`);
      } else {
        await updateLeadList(f.id, f);
        onSaved(`Updated list ${f.name}`);
      }
    } catch (err) {
      onError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal wide" onClick={(e) => e.stopPropagation()}>
        <header>{isNew ? "New List" : `Edit List ${f.name}`}</header>
        <form className="form" onSubmit={submit}>
          <div className="form-row">
            <label>
              Name
              <input value={f.name} placeholder="March web enquiries" onChange={(e) => set("name", e.target.value)} />
            </label>
            <label>
              Campaign <span className="hint-inline">(optional until campaigns land)</span>
              <input
                value={f.campaignId}
                placeholder="unassigned"
                onChange={(e) => set("campaignId", e.target.value)}
              />
            </label>
          </div>

          <label>
            Description
            <input value={f.description} onChange={(e) => set("description", e.target.value)} />
          </label>

          <div className="form-row">
            <label>
              Active
              <select value={f.active ? "yes" : "no"} onChange={(e) => set("active", e.target.value === "yes")}>
                <option value="yes">Yes — dialable</option>
                <option value="no">No — held back</option>
              </select>
            </label>
            <label>
              Expires on <span className="hint-inline">(blank = never)</span>
              <input type="date" value={f.expiresOn} onChange={(e) => set("expiresOn", e.target.value)} />
            </label>
          </div>

          <div className="ivr-opts">
            <div className="ivr-opts-head">
              <span>Custom fields</span>
              <button type="button" className="btn ghost small" onClick={addField}>
                + Add field
              </button>
            </div>
            {f.customFields.length === 0 ? (
              <p className="hint-inline">
                None. Add fields here to carry data this list needs beyond the standard contact
                details — they appear on the lead form and, from phase 3, on the agent screen.
              </p>
            ) : (
              f.customFields.map((c, i) => (
                <div className="ivr-opt-row" key={i}>
                  <input
                    value={c.name}
                    placeholder="policy_no"
                    onChange={(e) => setField(i, { name: e.target.value })}
                  />
                  <input
                    value={c.label}
                    placeholder="Policy #"
                    onChange={(e) => setField(i, { label: e.target.value })}
                  />
                  <select
                    value={c.type}
                    onChange={(e) => setField(i, { type: e.target.value as CustomField["type"] })}
                  >
                    <option value="text">text</option>
                    <option value="number">number</option>
                    <option value="date">date</option>
                    <option value="select">select</option>
                  </select>
                  {c.type === "select" ? (
                    <input
                      value={(c.options ?? []).join(", ")}
                      placeholder="option a, option b"
                      onChange={(e) =>
                        setField(i, {
                          options: e.target.value
                            .split(",")
                            .map((s) => s.trim())
                            .filter(Boolean),
                        })
                      }
                    />
                  ) : (
                    <span />
                  )}
                  <button type="button" className="btn danger small" onClick={() => removeField(i)}>
                    ✕
                  </button>
                </div>
              ))
            )}
          </div>

          {!isNew && f.statusCount && Object.keys(f.statusCount).length > 0 && (
            <p className="hint-inline">
              Breakdown:{" "}
              {Object.entries(f.statusCount)
                .map(([s, n]) => `${s} ${n.toLocaleString()}`)
                .join(" · ")}
            </p>
          )}

          <div className="form-actions">
            <button type="button" className="btn ghost" onClick={onClose}>
              Cancel
            </button>
            <button type="submit" className="btn" disabled={busy}>
              {busy ? "Saving…" : isNew ? "Create" : "Save"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// --- Lead editor -------------------------------------------------------------

function LeadForm({
  initial,
  lists,
  onClose,
  onSaved,
  onError,
}: {
  initial: Lead;
  lists: LeadList[];
  onClose: () => void;
  onSaved: (msg: string) => void;
  onError: (msg: string) => void;
}) {
  const [f, setF] = useState<Lead>(initial);
  const [busy, setBusy] = useState(false);
  const isNew = initial.id === 0;
  const set = <K extends keyof Lead>(k: K, v: Lead[K]) => setF((prev) => ({ ...prev, [k]: v }));
  const setCustom = (name: string, v: string) =>
    setF((prev) => ({ ...prev, custom: { ...(prev.custom ?? {}), [name]: v } }));

  const fields = lists.find((l) => l.id === f.listId)?.customFields ?? [];

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      if (isNew) {
        await createLead(f, "list");
        onSaved(`Added lead ${f.phoneNumber}`);
      } else {
        await updateLead(f.id, f);
        onSaved(`Updated lead ${f.phoneNumber}`);
      }
    } catch (err) {
      onError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal wide" onClick={(e) => e.stopPropagation()}>
        <header>{isNew ? "New Lead" : `Edit Lead ${f.phoneNumber}`}</header>
        <form className="form" onSubmit={submit}>
          <div className="form-row">
            <label>
              List
              <select value={f.listId} onChange={(e) => set("listId", parseInt(e.target.value, 10))}>
                {lists.map((l) => (
                  <option key={l.id} value={l.id}>
                    {l.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Status
              <input
                value={f.status}
                list="lead-statuses"
                onChange={(e) => set("status", e.target.value.toUpperCase())}
              />
              <datalist id="lead-statuses">
                {STATUSES.map((s) => (
                  <option key={s} value={s} />
                ))}
              </datalist>
            </label>
          </div>

          <div className="form-row">
            <label>
              Country / dial code
              <input value={f.phoneCode} placeholder="968" onChange={(e) => set("phoneCode", e.target.value)} />
            </label>
            <label>
              Phone number
              <input value={f.phoneNumber} placeholder="99123456" onChange={(e) => set("phoneNumber", e.target.value)} />
            </label>
            <label>
              Alt phone
              <input value={f.altPhone} onChange={(e) => set("altPhone", e.target.value)} />
            </label>
            <label>
              Alt phone 2
              <input value={f.altPhoneTwo} onChange={(e) => set("altPhoneTwo", e.target.value)} />
            </label>
          </div>

          <div className="form-row">
            <label>
              Title
              <input value={f.title} placeholder="Mr" onChange={(e) => set("title", e.target.value)} />
            </label>
            <label>
              First name
              <input value={f.firstName} onChange={(e) => set("firstName", e.target.value)} />
            </label>
            <label>
              Last name
              <input value={f.lastName} onChange={(e) => set("lastName", e.target.value)} />
            </label>
            <label>
              Email
              <input value={f.email} onChange={(e) => set("email", e.target.value)} />
            </label>
          </div>

          <div className="form-row">
            <label>
              Address
              <input value={f.address1} onChange={(e) => set("address1", e.target.value)} />
            </label>
            <label>
              Address 2
              <input value={f.address2} onChange={(e) => set("address2", e.target.value)} />
            </label>
          </div>

          <div className="form-row">
            <label>
              City
              <input value={f.city} onChange={(e) => set("city", e.target.value)} />
            </label>
            <label>
              State / region
              <input value={f.state} onChange={(e) => set("state", e.target.value)} />
            </label>
            <label>
              Postal code
              <input value={f.postalCode} onChange={(e) => set("postalCode", e.target.value)} />
            </label>
            <label>
              Country
              <input value={f.country} onChange={(e) => set("country", e.target.value)} />
            </label>
          </div>

          <div className="form-row">
            <label>
              Vendor lead code <span className="hint-inline">(your own key)</span>
              <input value={f.vendorLeadCode} onChange={(e) => set("vendorLeadCode", e.target.value)} />
            </label>
            <label>
              Source
              <input value={f.sourceId} onChange={(e) => set("sourceId", e.target.value)} />
            </label>
            <label>
              Owner <span className="hint-inline">(assigned agent)</span>
              <input value={f.owner} onChange={(e) => set("owner", e.target.value)} />
            </label>
          </div>

          {fields.length > 0 && (
            <div className="form-row">
              {fields.map((c) => (
                <label key={c.name}>
                  {c.label}
                  {c.type === "select" ? (
                    <select
                      value={String(f.custom?.[c.name] ?? "")}
                      onChange={(e) => setCustom(c.name, e.target.value)}
                    >
                      <option value=""></option>
                      {(c.options ?? []).map((o) => (
                        <option key={o} value={o}>
                          {o}
                        </option>
                      ))}
                    </select>
                  ) : (
                    <input
                      type={c.type === "number" ? "number" : c.type === "date" ? "date" : "text"}
                      value={String(f.custom?.[c.name] ?? "")}
                      onChange={(e) => setCustom(c.name, e.target.value)}
                    />
                  )}
                </label>
              ))}
            </div>
          )}

          <label>
            Comments
            <textarea rows={3} value={f.comments} onChange={(e) => set("comments", e.target.value)} />
          </label>

          {!isNew && (
            <p className="hint-inline">
              Called {f.calledCount} time(s)
              {f.lastCalledAt ? `, last on ${new Date(f.lastCalledAt).toLocaleString()}` : ""}. Call
              history is written by the dialer and is not editable here.
            </p>
          )}

          <div className="form-actions">
            <button type="button" className="btn ghost" onClick={onClose}>
              Cancel
            </button>
            <button type="submit" className="btn" disabled={busy}>
              {busy ? "Saving…" : isNew ? "Add lead" : "Save"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// --- CSV import --------------------------------------------------------------

// Standard columns an import may carry, and the header spellings accepted for
// each. Lead files come from every imaginable CRM, so matching is generous:
// case, spaces, underscores and hyphens are all ignored.
const COLUMN_ALIASES: Record<keyof Lead | string, string[]> = {
  phoneNumber: ["phonenumber", "phone", "number", "mobile", "msisdn", "telephone", "tel"],
  phoneCode: ["phonecode", "countrycode", "dialcode", "cc"],
  altPhone: ["altphone", "alternatephone", "phone2", "secondphone"],
  altPhoneTwo: ["altphonetwo", "altphone2", "phone3", "thirdphone"],
  title: ["title", "salutation"],
  firstName: ["firstname", "first", "givenname", "fname"],
  lastName: ["lastname", "last", "surname", "familyname", "lname"],
  email: ["email", "emailaddress", "mail"],
  address1: ["address1", "address", "street", "addressline1"],
  address2: ["address2", "addressline2"],
  city: ["city", "town"],
  state: ["state", "region", "province", "governorate"],
  postalCode: ["postalcode", "postcode", "zip", "zipcode"],
  country: ["country"],
  comments: ["comments", "comment", "notes", "note"],
  vendorLeadCode: ["vendorleadcode", "vendorcode", "leadcode", "externalid", "customerid"],
  sourceId: ["sourceid", "source", "campaignsource"],
  owner: ["owner", "agent", "assignedto"],
  status: ["status", "disposition"],
};

function normaliseHeader(h: string): string {
  return h.toLowerCase().replace(/[\s_\-.]/g, "");
}

// splitCSVLine handles quoted fields, so addresses and comments may contain
// commas and escaped ("") quotes.
function splitCSVLine(line: string): string[] {
  const out: string[] = [];
  let cur = "";
  let inQuotes = false;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (inQuotes) {
      if (ch === '"') {
        if (line[i + 1] === '"') {
          cur += '"';
          i++;
        } else inQuotes = false;
      } else cur += ch;
    } else if (ch === '"') {
      inQuotes = true;
    } else if (ch === ",") {
      out.push(cur.trim());
      cur = "";
    } else {
      cur += ch;
    }
  }
  out.push(cur.trim());
  return out;
}

interface ParseResult {
  rows: Partial<Lead>[];
  mapped: string[];
  ignored: string[];
  errors: string[];
}

// parseLeadCSV is header-driven rather than positional: the first row names the
// columns, in any order. Headers matching one of the list's custom fields are
// carried into `custom`; anything unrecognised is reported and skipped rather
// than silently dropped.
function parseLeadCSV(text: string, customFields: CustomField[]): ParseResult {
  const lines = text.split(/\r?\n/).filter((l) => l.trim() !== "");
  if (lines.length === 0) return { rows: [], mapped: [], ignored: [], errors: ["The file is empty."] };

  const header = splitCSVLine(lines[0]).map(normaliseHeader);
  const customByName = new Map(customFields.map((c) => [normaliseHeader(c.name), c.name]));

  // Resolve each header cell to a lead field, a custom field, or nothing.
  const mapping: ({ kind: "std"; key: string } | { kind: "custom"; key: string } | null)[] = header.map((h) => {
    for (const [field, aliases] of Object.entries(COLUMN_ALIASES)) {
      if (aliases.includes(h)) return { kind: "std", key: field };
    }
    const c = customByName.get(h);
    if (c) return { kind: "custom", key: c };
    return null;
  });

  const mapped = mapping.filter(Boolean).map((m) => m!.key);
  const ignored = header.filter((_, i) => mapping[i] === null);
  const errors: string[] = [];

  if (!mapped.includes("phoneNumber")) {
    errors.push(
      `No phone number column found. The header row must include one of: ${COLUMN_ALIASES.phoneNumber.join(", ")}.`
    );
    return { rows: [], mapped, ignored, errors };
  }

  const rows: Partial<Lead>[] = [];
  for (let i = 1; i < lines.length; i++) {
    const cells = splitCSVLine(lines[i]);
    const lead: Partial<Lead> = { custom: {} };
    mapping.forEach((m, col) => {
      if (!m) return;
      const v = cells[col] ?? "";
      if (v === "") return;
      if (m.kind === "custom") (lead.custom as Record<string, unknown>)[m.key] = v;
      else (lead as Record<string, unknown>)[m.key] = m.key === "status" ? v.toUpperCase() : v;
    });
    if (!lead.phoneNumber) {
      errors.push(`Row ${i + 1}: no phone number — skipped.`);
      continue;
    }
    rows.push(lead);
  }
  return { rows, mapped, ignored, errors };
}

const TEMPLATE =
  "phone,first_name,last_name,email,city,state,postal_code,vendor_lead_code,source\n" +
  "96899123456,Ahmed,Al Balushi,ahmed@example.com,Muscat,Muscat,100,CUST-1001,web\n" +
  "96899123457,Fatma,Al Harthy,fatma@example.com,Sohar,Batinah,311,CUST-1002,web\n";

function downloadTemplate() {
  const url = URL.createObjectURL(new Blob([TEMPLATE], { type: "text/csv" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = "leads-template.csv";
  a.click();
  URL.revokeObjectURL(url);
}

function ImportModal({
  lists,
  initialList,
  onClose,
  onDone,
  onError,
}: {
  lists: LeadList[];
  initialList: LeadList;
  onClose: () => void;
  onDone: (msg: string) => void;
  onError: (msg: string) => void;
}) {
  const [listId, setListId] = useState(initialList.id);
  const [dupScope, setDupScope] = useState<DupScope>("list");
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [report, setReport] = useState<string[] | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  const list = lists.find((l) => l.id === listId) ?? initialList;
  const preview = useMemo(
    () => (text.trim() ? parseLeadCSV(text, list.customFields) : null),
    [text, list]
  );

  const onFile = (file: File) => {
    const reader = new FileReader();
    reader.onload = () => setText(String(reader.result || ""));
    reader.readAsText(file);
  };

  const submit = async () => {
    const parsed = parseLeadCSV(text, list.customFields);
    if (parsed.rows.length === 0) {
      onError(parsed.errors[0] || "Nothing to import — paste rows or choose a CSV file.");
      return;
    }
    setBusy(true);
    try {
      const res = await importLeads(listId, parsed.rows, dupScope);
      const failedRows = res.results.filter((r) => !r.ok && r.error);
      setReport([
        `Imported ${res.created} of ${parsed.rows.length} lead(s) into "${list.name}".`,
        ...(res.duplicates > 0 ? [`${res.duplicates} skipped as duplicates.`] : []),
        ...parsed.errors,
        ...failedRows.slice(0, 50).map((r) => `Row ${r.row} (${r.phone}): ${r.error}`),
        ...(failedRows.length > 50 ? [`…and ${failedRows.length - 50} more.`] : []),
      ]);
      if (res.created > 0) onDone(`Imported ${res.created} lead(s) into ${list.name}.`);
    } catch (e) {
      onError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal wide" onClick={(e) => e.stopPropagation()}>
        <header>Import Leads</header>
        <div className="form">
          <div className="form-row">
            <label>
              Into list
              <select value={listId} onChange={(e) => setListId(parseInt(e.target.value, 10))}>
                {lists.map((l) => (
                  <option key={l.id} value={l.id}>
                    {l.name} ({l.leadCount.toLocaleString()} leads)
                  </option>
                ))}
              </select>
            </label>
            <label>
              Skip duplicates
              <select value={dupScope} onChange={(e) => setDupScope(e.target.value as DupScope)}>
                <option value="list">Already in this list</option>
                <option value="campaign">Already in this campaign's lists</option>
                <option value="system">Already anywhere in the system</option>
                <option value="none">Don't check — import everything</option>
              </select>
            </label>
          </div>

          <p className="hint-inline">
            The first row must be a header naming the columns; order does not matter. A phone
            number column is required — everything else is optional. Columns named after this
            list's custom fields are imported into them; unrecognised columns are ignored.
          </p>

          <div className="row-action">
            <button type="button" className="btn ghost small" onClick={downloadTemplate}>
              ↓ Download template
            </button>
            <button type="button" className="btn ghost small" onClick={() => fileRef.current?.click()}>
              Choose CSV file…
            </button>
            <input
              ref={fileRef}
              type="file"
              accept=".csv,text/csv"
              style={{ display: "none" }}
              onChange={(e) => e.target.files?.[0] && onFile(e.target.files[0])}
            />
          </div>

          <label>
            CSV content
            <textarea
              rows={8}
              className="mono"
              placeholder={TEMPLATE}
              value={text}
              onChange={(e) => setText(e.target.value)}
            />
          </label>

          {preview && (
            <p className="hint-inline">
              {preview.rows.length.toLocaleString()} row(s) ready · mapped:{" "}
              {preview.mapped.join(", ") || "nothing"}
              {preview.ignored.length > 0 && ` · ignored: ${preview.ignored.join(", ")}`}
              {preview.errors.length > 0 && ` · ${preview.errors.length} row problem(s)`}
            </p>
          )}

          {report && (
            <div className="bulk-report">
              {report.map((l, i) => (
                <div key={i}>{l}</div>
              ))}
            </div>
          )}

          <div className="form-actions">
            <button type="button" className="btn ghost" onClick={onClose}>
              {report ? "Close" : "Cancel"}
            </button>
            <button
              type="button"
              className="btn"
              disabled={busy || !preview || preview.rows.length === 0}
              onClick={submit}
            >
              {busy ? "Importing…" : "Import"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
