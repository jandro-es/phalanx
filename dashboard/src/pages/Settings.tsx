// =============================================================================
// Phalanx Dashboard — Settings
// =============================================================================
// Token entry UI. The bearer token is stored in localStorage under
// `phalanx_token` and read by useApi() on every request.

import React, { useEffect, useState } from "react";

const API_BASE = import.meta.env.VITE_PHALANX_API_URL ?? "http://localhost:3100";

export function Settings() {
  const [token, setToken] = useState("");
  const [saved, setSaved] = useState(false);
  const [healthState, setHealthState] = useState<
    | { status: "loading" }
    | { status: "ok"; database: boolean }
    | { status: "unauthorized" }
    | { status: "error"; message: string }
  >({ status: "loading" });

  useEffect(() => {
    setToken(localStorage.getItem("phalanx_token") ?? "");
  }, []);

  // Probe a protected endpoint to surface 401s from a stale/missing token.
  // /api/agents is cheap and always present.
  const probe = async (tok: string) => {
    setHealthState({ status: "loading" });
    try {
      const res = await fetch(`${API_BASE}/api/agents`, {
        headers: tok ? { Authorization: `Bearer ${tok}` } : {},
      });
      if (res.status === 401) {
        setHealthState({ status: "unauthorized" });
        return;
      }
      if (!res.ok) {
        setHealthState({ status: "error", message: `HTTP ${res.status}` });
        return;
      }
      // /health gives us DB status — best-effort only.
      const h = await fetch(`${API_BASE}/health`).then((r) => r.json()).catch(() => null);
      setHealthState({ status: "ok", database: h?.database ?? false });
    } catch (e: any) {
      setHealthState({ status: "error", message: String(e.message ?? e) });
    }
  };

  useEffect(() => {
    probe(localStorage.getItem("phalanx_token") ?? "");
  }, []);

  const save = (e: React.FormEvent) => {
    e.preventDefault();
    if (token) {
      localStorage.setItem("phalanx_token", token);
    } else {
      localStorage.removeItem("phalanx_token");
    }
    setSaved(true);
    setTimeout(() => setSaved(false), 2000);
    probe(token);
  };

  const clear = () => {
    localStorage.removeItem("phalanx_token");
    setToken("");
    probe("");
  };

  return (
    <div className="max-w-2xl space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-gray-900 mb-1">Settings</h1>
        <p className="text-sm text-gray-500">
          Configure how this dashboard talks to the Phalanx server. The token is stored locally in
          your browser (<code>localStorage.phalanx_token</code>) and never sent anywhere except your
          server's API.
        </p>
      </div>

      <form onSubmit={save} className="bg-white rounded-lg border p-4 space-y-3">
        <label className="block text-sm font-medium text-gray-700">API bearer token</label>
        <input
          value={token}
          onChange={(e) => setToken(e.target.value)}
          type="password"
          autoComplete="off"
          placeholder="phx_xxx..."
          className="w-full border rounded px-3 py-2 text-sm font-mono"
        />
        <p className="text-xs text-gray-500">
          One of the values from <code>PHALANX_API_TOKENS</code> on the server. Empty disables auth
          and only works against a server that also has no tokens configured (local dev).
        </p>
        <div className="flex items-center gap-3">
          <button className="bg-blue-600 text-white text-sm px-4 py-2 rounded hover:bg-blue-700">
            Save
          </button>
          <button type="button" onClick={clear} className="text-sm text-gray-600 hover:text-gray-900">
            Clear
          </button>
          {saved && <span className="text-xs text-green-700">Saved.</span>}
        </div>
      </form>

      <div className="bg-white rounded-lg border p-4 space-y-2">
        <h2 className="text-sm font-semibold text-gray-700">Server status</h2>
        <div className="text-sm">
          <span className="text-gray-500">API base: </span>
          <code className="text-xs">{API_BASE}</code>
        </div>
        <div className="text-sm">
          <span className="text-gray-500">Connection: </span>
          {healthState.status === "loading" && <span className="text-gray-400">Checking…</span>}
          {healthState.status === "ok" && (
            <span className="text-green-700">
              Authenticated · DB {healthState.database ? "OK" : "down"}
            </span>
          )}
          {healthState.status === "unauthorized" && (
            <span className="text-amber-700">401 — token missing or invalid</span>
          )}
          {healthState.status === "error" && (
            <span className="text-red-700">{healthState.message}</span>
          )}
        </div>
      </div>
    </div>
  );
}
