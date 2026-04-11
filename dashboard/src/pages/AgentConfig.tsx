// =============================================================================
// Phalanx Dashboard — Agent Configuration
// =============================================================================

import React, { useEffect, useState } from "react";
import { useApi } from "../App";

export function AgentConfig() {
  const api = useApi();
  const [agents, setAgents] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const refresh = () =>
    api
      .get("/api/agents")
      .then((data) => setAgents(data.agents ?? []))
      .catch((e) => setError(String(e)));

  useEffect(() => {
    refresh().finally(() => setLoading(false));
  }, []);

  const toggleAgent = async (id: string, enabled: boolean) => {
    if (!enabled) {
      await api.post(`/api/agents/${id}`, { enabled: false });
    }
    await refresh();
  };

  if (loading) return <div className="text-gray-500">Loading agents...</div>;
  if (error) return <div className="text-red-600">Failed to load agents: {error}</div>;

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Review Agents</h1>
      <div className="bg-white rounded-lg border overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="bg-gray-50 text-left text-xs text-gray-500">
              <th className="px-4 py-3">Agent</th>
              <th className="px-4 py-3">Skill</th>
              <th className="px-4 py-3">Provider / Model</th>
              <th className="px-4 py-3">Temperature</th>
              <th className="px-4 py-3">Priority</th>
              <th className="px-4 py-3">Status</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100">
            {agents.length === 0 && (
              <tr>
                <td colSpan={6} className="px-4 py-6 text-center text-sm text-gray-400">
                  No agents configured yet. Use <code>phalanx agent</code> or{" "}
                  <code>POST /api/agents</code> to create one.
                </td>
              </tr>
            )}
            {agents.map((a) => (
              <tr key={a.id} className="hover:bg-gray-50">
                <td className="px-4 py-3 font-medium">{a.name}</td>
                <td className="px-4 py-3">
                  <span className="bg-indigo-50 text-indigo-700 text-xs px-2 py-0.5 rounded">
                    {a.skill_slug}
                  </span>
                </td>
                <td className="px-4 py-3 text-gray-600">
                  {a.provider_name} / {a.model_override ?? "default"}
                </td>
                <td className="px-4 py-3 text-gray-500">{a.temperature}</td>
                <td className="px-4 py-3 text-gray-500">{a.priority}</td>
                <td className="px-4 py-3">
                  <button
                    onClick={() => toggleAgent(a.id, !a.enabled)}
                    className={`text-xs px-2 py-1 rounded ${
                      a.enabled
                        ? "bg-green-100 text-green-700"
                        : "bg-gray-100 text-gray-500"
                    }`}
                  >
                    {a.enabled ? "Enabled" : "Disabled"}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
