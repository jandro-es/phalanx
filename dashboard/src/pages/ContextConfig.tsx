// =============================================================================
// Phalanx Dashboard — Context Documents
// =============================================================================
// Lists, creates, and deletes context documents bound to agents. The list
// endpoint omits the `content` body so this page only loads bodies on demand.

import React, { useEffect, useState } from "react";
import { useApi } from "../App";

type ContextRow = {
  id: string;
  name: string;
  doc_type: string;
  tags: string[];
  created_at: string;
};

const DOC_TYPES = ["guideline", "non-negotiable", "reference", "example"];

export function ContextConfig() {
  const api = useApi();
  const [rows, setRows] = useState<ContextRow[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({
    name: "",
    docType: "guideline",
    tags: "",
    content: "",
  });

  const refresh = () =>
    api
      .get("/api/contexts")
      .then((data: any) => setRows(data.contexts ?? []))
      .catch((e: Error) => setError(String(e.message)));

  useEffect(() => {
    refresh().finally(() => setLoading(false));
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setCreating(true);
    try {
      await api.post("/api/contexts", {
        name: form.name,
        docType: form.docType,
        content: form.content,
        tags: form.tags
          .split(",")
          .map((t) => t.trim())
          .filter(Boolean),
      });
      setForm({ name: "", docType: "guideline", tags: "", content: "" });
      await refresh();
    } catch (err: any) {
      setError(String(err.message ?? err));
    } finally {
      setCreating(false);
    }
  };

  const remove = async (id: string) => {
    if (!confirm("Delete this context document? Bindings to agents will also be removed.")) return;
    try {
      await api.del(`/api/contexts/${id}`);
      await refresh();
    } catch (err: any) {
      setError(String(err.message ?? err));
    }
  };

  if (loading) return <div className="text-gray-500">Loading contexts...</div>;

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-gray-900 mb-1">Context Documents</h1>
        <p className="text-sm text-gray-500">
          Guidelines, non-negotiables, references, and examples that get spliced into the system
          prompt of any agent they're bound to.
        </p>
      </div>

      {error && (
        <div className="bg-red-50 border border-red-200 text-red-700 px-3 py-2 rounded text-sm">
          {error}
        </div>
      )}

      <div className="bg-white rounded-lg border overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="bg-gray-50 text-left text-xs text-gray-500">
              <th className="px-4 py-3">Name</th>
              <th className="px-4 py-3">Type</th>
              <th className="px-4 py-3">Tags</th>
              <th className="px-4 py-3">Created</th>
              <th className="px-4 py-3"></th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100">
            {rows.length === 0 && (
              <tr>
                <td colSpan={5} className="px-4 py-6 text-center text-sm text-gray-400">
                  No context documents yet.
                </td>
              </tr>
            )}
            {rows.map((c) => (
              <tr key={c.id} className="hover:bg-gray-50">
                <td className="px-4 py-3 font-medium">{c.name}</td>
                <td className="px-4 py-3">
                  <span className="text-xs bg-indigo-50 text-indigo-700 px-2 py-0.5 rounded">
                    {c.doc_type}
                  </span>
                </td>
                <td className="px-4 py-3 text-gray-500 text-xs">{c.tags.join(", ")}</td>
                <td className="px-4 py-3 text-gray-400 text-xs">
                  {new Date(c.created_at).toLocaleString()}
                </td>
                <td className="px-4 py-3 text-right">
                  <button
                    onClick={() => remove(c.id)}
                    className="text-xs text-red-600 hover:text-red-800"
                  >
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <form onSubmit={submit} className="bg-white rounded-lg border p-4 space-y-3">
        <h2 className="text-sm font-semibold text-gray-700">Add a context document</h2>
        <div className="grid grid-cols-2 gap-3">
          <input
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
            placeholder="Name (e.g. 'Security non-negotiables')"
            className="border rounded px-3 py-2 text-sm"
            required
          />
          <select
            value={form.docType}
            onChange={(e) => setForm({ ...form, docType: e.target.value })}
            className="border rounded px-3 py-2 text-sm"
          >
            {DOC_TYPES.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </div>
        <input
          value={form.tags}
          onChange={(e) => setForm({ ...form, tags: e.target.value })}
          placeholder="Tags (comma-separated)"
          className="w-full border rounded px-3 py-2 text-sm"
        />
        <textarea
          value={form.content}
          onChange={(e) => setForm({ ...form, content: e.target.value })}
          placeholder="Document body (Markdown welcome)"
          rows={6}
          className="w-full border rounded px-3 py-2 text-sm font-mono"
          required
        />
        <button
          disabled={creating}
          className="bg-blue-600 text-white text-sm px-4 py-2 rounded hover:bg-blue-700 disabled:opacity-50"
        >
          {creating ? "Saving..." : "Save"}
        </button>
      </form>
    </div>
  );
}
