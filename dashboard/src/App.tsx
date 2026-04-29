// =============================================================================
// Phalanx — Dashboard App
// =============================================================================

import React from "react";
import { BrowserRouter, Routes, Route, NavLink } from "react-router-dom";
import { Sessions } from "./pages/Sessions";
import { SessionDetail } from "./pages/SessionDetail";
import { AuditTrail } from "./pages/AuditTrail";
import { AgentConfig } from "./pages/AgentConfig";
import { ProviderConfig } from "./pages/ProviderConfig";
import { ContextConfig } from "./pages/ContextConfig";
import { Settings } from "./pages/Settings";

const API_BASE = import.meta.env.VITE_PHALANX_API_URL ?? "http://localhost:3100";

export function useApi() {
  const token = localStorage.getItem("phalanx_token") ?? "";
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token) headers["Authorization"] = `Bearer ${token}`;

  const send = async (method: string, path: string, body?: unknown) => {
    const res = await fetch(`${API_BASE}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (res.status === 401) {
      throw new Error("API 401: missing or invalid bearer token (Settings → API token).");
    }
    if (!res.ok) throw new Error(`API ${res.status}: ${await res.text()}`);
    if (res.status === 204) return null;
    const text = await res.text();
    return text ? JSON.parse(text) : null;
  };

  return {
    get: (path: string) => send("GET", path),
    post: (path: string, body: unknown) => send("POST", path, body),
    put: (path: string, body: unknown) => send("PUT", path, body),
    del: (path: string) => send("DELETE", path),
  };
}

export default function App() {
  return (
    <BrowserRouter>
      <div className="min-h-screen bg-gray-50">
        {/* Navigation */}
        <nav className="bg-white border-b border-gray-200 px-6 py-3">
          <div className="max-w-7xl mx-auto flex items-center gap-8">
            <div className="flex items-center gap-2">
              <span className="text-xl">🛡️</span>
              <span className="font-bold text-lg text-gray-900">Phalanx</span>
            </div>
            <div className="flex gap-4">
              <NavLink to="/" className={({ isActive }) =>
                `px-3 py-1 rounded text-sm ${isActive ? "bg-blue-100 text-blue-700" : "text-gray-600 hover:text-gray-900"}`
              }>Reviews</NavLink>
              <NavLink to="/audit" className={({ isActive }) =>
                `px-3 py-1 rounded text-sm ${isActive ? "bg-blue-100 text-blue-700" : "text-gray-600 hover:text-gray-900"}`
              }>Audit Trail</NavLink>
              <NavLink to="/agents" className={({ isActive }) =>
                `px-3 py-1 rounded text-sm ${isActive ? "bg-blue-100 text-blue-700" : "text-gray-600 hover:text-gray-900"}`
              }>Agents</NavLink>
              <NavLink to="/providers" className={({ isActive }) =>
                `px-3 py-1 rounded text-sm ${isActive ? "bg-blue-100 text-blue-700" : "text-gray-600 hover:text-gray-900"}`
              }>Providers</NavLink>
              <NavLink to="/contexts" className={({ isActive }) =>
                `px-3 py-1 rounded text-sm ${isActive ? "bg-blue-100 text-blue-700" : "text-gray-600 hover:text-gray-900"}`
              }>Contexts</NavLink>
            </div>
            <div className="flex-1" />
            <NavLink to="/settings" className={({ isActive }) =>
              `px-3 py-1 rounded text-sm ${isActive ? "bg-blue-100 text-blue-700" : "text-gray-500 hover:text-gray-900"}`
            }>Settings</NavLink>
          </div>
        </nav>

        {/* Routes */}
        <main className="max-w-7xl mx-auto px-6 py-8">
          <Routes>
            <Route path="/" element={<Sessions />} />
            <Route path="/sessions/:id" element={<SessionDetail />} />
            <Route path="/audit" element={<AuditTrail />} />
            <Route path="/agents" element={<AgentConfig />} />
            <Route path="/providers" element={<ProviderConfig />} />
            <Route path="/contexts" element={<ContextConfig />} />
            <Route path="/settings" element={<Settings />} />
          </Routes>
        </main>
      </div>
    </BrowserRouter>
  );
}
