// =============================================================================
// Phalanx Dashboard — Audit Trail
// =============================================================================

import React, { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useApi } from "../App";

interface AuditEntry {
  id: number;
  event_type: string;
  session_id: string | null;
  agent_id: string | null;
  actor: string;
  payload: Record<string, unknown>;
  created_at: string;
}

const EVENT_COLORS: Record<string, string> = {
  "session.": "text-blue-700 bg-blue-50",
  "agent.": "text-purple-700 bg-purple-50",
  "llm.": "text-gray-600 bg-gray-50",
  "report.": "text-green-700 bg-green-50",
  "decision.": "text-orange-700 bg-orange-50",
  "config.": "text-indigo-700 bg-indigo-50",
};

function getEventColor(eventType: string): string {
  for (const [prefix, color] of Object.entries(EVENT_COLORS)) {
    if (eventType.startsWith(prefix)) return color;
  }
  return "text-gray-600 bg-gray-50";
}

export function AuditTrail() {
  const api = useApi();
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [filters, setFilters] = useState({
    sessionId: "",
    eventType: "",
    actor: "",
  });

  const loadEntries = async () => {
    setLoading(true);
    setError(null);
    const params = new URLSearchParams({ limit: "200" });
    if (filters.sessionId) params.set("sessionId", filters.sessionId);
    if (filters.eventType) params.set("eventType", filters.eventType);
    if (filters.actor) params.set("actor", filters.actor);

    try {
      const data = await api.get(`/api/audit?${params}`);
      setEntries(data.entries ?? []);
    } catch (e) {
      setError(String(e));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { loadEntries(); }, []);

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Audit Trail</h1>

      {/* Filters */}
      <div className="bg-white rounded-lg border p-4 mb-6 flex gap-4 items-end">
        <div>
          <label className="text-xs text-gray-500 block mb-1">Session ID</label>
          <input
            value={filters.sessionId}
            onChange={(e) => setFilters({ ...filters, sessionId: e.target.value })}
            placeholder="UUID..."
            className="border rounded px-3 py-1.5 text-sm w-64"
          />
        </div>
        <div>
          <label className="text-xs text-gray-500 block mb-1">Event Type</label>
          <select
            value={filters.eventType}
            onChange={(e) => setFilters({ ...filters, eventType: e.target.value })}
            className="border rounded px-3 py-1.5 text-sm"
          >
            <option value="">All</option>
            <option value="session.created">session.created</option>
            <option value="session.completed">session.completed</option>
            <option value="agent.completed">agent.completed</option>
            <option value="llm.request">llm.request</option>
            <option value="llm.response">llm.response</option>
            <option value="decision.approve">decision.approve</option>
            <option value="decision.request_changes">decision.request_changes</option>
            <option value="config.agent.created">config.agent.created</option>
            <option value="config.skill.updated">config.skill.updated</option>
          </select>
        </div>
        <div>
          <label className="text-xs text-gray-500 block mb-1">Actor</label>
          <input
            value={filters.actor}
            onChange={(e) => setFilters({ ...filters, actor: e.target.value })}
            placeholder="system, webhook, user..."
            className="border rounded px-3 py-1.5 text-sm w-48"
          />
        </div>
        <button
          onClick={loadEntries}
          className="px-4 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700"
        >
          Search
        </button>
      </div>

      {/* Results */}
      {loading ? (
        <div className="text-gray-500">Loading...</div>
      ) : error ? (
        <div className="text-red-600">Failed to load audit trail: {error}</div>
      ) : (
        <div className="bg-white rounded-lg border overflow-hidden">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-gray-50 text-left text-xs text-gray-500">
                <th className="px-4 py-2">ID</th>
                <th className="px-4 py-2">Time</th>
                <th className="px-4 py-2">Event</th>
                <th className="px-4 py-2">Actor</th>
                <th className="px-4 py-2">Session</th>
                <th className="px-4 py-2">Payload</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {entries.length === 0 && (
                <tr>
                  <td colSpan={6} className="px-4 py-6 text-center text-sm text-gray-400">
                    No audit entries match the current filters.
                  </td>
                </tr>
              )}
              {entries.map((e) => (
                <tr key={e.id} className="hover:bg-gray-50">
                  <td className="px-4 py-2 text-xs text-gray-400 font-mono">{e.id}</td>
                  <td className="px-4 py-2 text-xs text-gray-500 whitespace-nowrap">
                    {new Date(e.created_at).toLocaleString()}
                  </td>
                  <td className="px-4 py-2">
                    <span className={`text-xs px-2 py-0.5 rounded ${getEventColor(e.event_type)}`}>
                      {e.event_type}
                    </span>
                  </td>
                  <td className="px-4 py-2 text-xs text-gray-600">{e.actor}</td>
                  <td className="px-4 py-2 text-xs">
                    {e.session_id ? (
                      <Link to={`/sessions/${e.session_id}`} className="text-blue-600 hover:underline font-mono">
                        {e.session_id.slice(0, 8)}
                      </Link>
                    ) : (
                      <span className="text-gray-300">—</span>
                    )}
                  </td>
                  <td className="px-4 py-2">
                    <details>
                      <summary className="cursor-pointer text-xs text-gray-400 hover:text-gray-600">
                        {JSON.stringify(e.payload).slice(0, 60)}…
                      </summary>
                      <pre className="mt-2 text-xs bg-gray-50 p-2 rounded max-w-lg overflow-auto">
                        {JSON.stringify(e.payload, null, 2)}
                      </pre>
                    </details>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="px-4 py-3 bg-gray-50 text-xs text-gray-500">
            {entries.length} entries shown
          </div>
        </div>
      )}
    </div>
  );
}
