// =============================================================================
// Phalanx Dashboard — Sessions List
// =============================================================================

import React, { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useApi } from "../App";

const VERDICT_BADGE: Record<string, { bg: string; text: string; label: string }> = {
  pass: { bg: "bg-green-100", text: "text-green-800", label: "✅ PASS" },
  warn: { bg: "bg-yellow-100", text: "text-yellow-800", label: "⚠️ WARN" },
  fail: { bg: "bg-red-100", text: "text-red-800", label: "🔴 FAIL" },
  error: { bg: "bg-red-100", text: "text-red-800", label: "❌ ERROR" },
  not_applicable: { bg: "bg-gray-100", text: "text-gray-600", label: "⏭️ N/A" },
};

const STATUS_BADGE: Record<string, string> = {
  pending: "bg-gray-200 text-gray-700",
  queued: "bg-blue-100 text-blue-700",
  running: "bg-blue-200 text-blue-800",
  completed: "bg-green-100 text-green-700",
  failed: "bg-red-100 text-red-700",
  cancelled: "bg-gray-200 text-gray-500",
};

interface Session {
  id: string;
  external_pr_id: string;
  platform: string;
  repository_full_name: string;
  pr_number: number;
  pr_title: string;
  pr_author: string;
  status: string;
  overall_verdict: string | null;
  started_at: string;
  completed_at: string | null;
}

export function Sessions() {
  const api = useApi();
  const [sessions, setSessions] = useState<Session[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    api.get("/api/reviews?limit=50").then((data) => {
      setSessions(data.sessions);
      setLoading(false);
    });
  }, []);

  if (loading) return <div className="text-gray-500">Loading reviews...</div>;

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">Review Sessions</h1>

      <div className="bg-white rounded-lg border border-gray-200 overflow-hidden">
        <table className="w-full">
          <thead>
            <tr className="bg-gray-50 text-left text-sm text-gray-600">
              <th className="px-4 py-3">PR</th>
              <th className="px-4 py-3">Repository</th>
              <th className="px-4 py-3">Author</th>
              <th className="px-4 py-3">Status</th>
              <th className="px-4 py-3">Verdict</th>
              <th className="px-4 py-3">Time</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100">
            {sessions.map((s) => {
              const verdict = s.overall_verdict ? VERDICT_BADGE[s.overall_verdict] : null;
              return (
                <tr key={s.id} className="hover:bg-gray-50">
                  <td className="px-4 py-3">
                    <Link to={`/sessions/${s.id}`} className="text-blue-600 hover:underline font-medium">
                      #{s.pr_number}
                    </Link>
                    <div className="text-xs text-gray-500 truncate max-w-xs">{s.pr_title}</div>
                  </td>
                  <td className="px-4 py-3 text-sm text-gray-700">{s.repository_full_name}</td>
                  <td className="px-4 py-3 text-sm text-gray-700">{s.pr_author}</td>
                  <td className="px-4 py-3">
                    <span className={`text-xs px-2 py-1 rounded-full ${STATUS_BADGE[s.status] ?? ""}`}>
                      {s.status}
                    </span>
                  </td>
                  <td className="px-4 py-3">
                    {verdict && (
                      <span className={`text-xs px-2 py-1 rounded-full ${verdict.bg} ${verdict.text}`}>
                        {verdict.label}
                      </span>
                    )}
                  </td>
                  <td className="px-4 py-3 text-sm text-gray-500">
                    {new Date(s.started_at).toLocaleString()}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
