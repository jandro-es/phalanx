// =============================================================================
// Phalanx Dashboard — Session Detail + Approval Flow
// =============================================================================

import React, { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { useApi } from "../App";

interface Report {
  id: string;
  skill_slug: string;
  model_used: string;
  provider_name: string;
  verdict: string;
  report_md: string;
  input_tokens: number;
  output_tokens: number;
  latency_ms: number;
  cost_estimate_usd: number | null;
  findings: any[];
}

interface Decision {
  id: string;
  decision: string;
  engineer_name: string;
  engineer_id: string;
  justification: string | null;
  overridden_verdicts: any[];
  decided_at: string;
}

export function SessionDetail() {
  const { id } = useParams<{ id: string }>();
  const api = useApi();
  const [data, setData] = useState<any>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showApproval, setShowApproval] = useState(false);
  const [decision, setDecision] = useState<"approve" | "request_changes" | "defer">("approve");
  const [justification, setJustification] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!id) return;
    let cancelled = false;
    const poll = async () => {
      try {
        const result = await api.get(`/api/reviews/${id}`);
        if (cancelled) return;
        setData(result);
        setLoading(false);
        const status = result?.session?.status;
        if (status === "running" || status === "queued") {
          setTimeout(poll, 3000);
        }
      } catch (e) {
        if (!cancelled) {
          setError(String(e));
          setLoading(false);
        }
      }
    };
    poll();
    return () => {
      cancelled = true;
    };
  }, [id]);

  const submitDecision = async () => {
    setSubmitting(true);
    try {
      await api.post(`/api/decisions/${id}`, {
        decision,
        engineerId: "dashboard-user",
        engineerName: "Dashboard User",
        justification: justification || undefined,
        overriddenVerdicts: [],
      });
      const result = await api.get(`/api/reviews/${id}`);
      setData(result);
      setShowApproval(false);
    } finally {
      setSubmitting(false);
    }
  };

  if (loading) return <div className="text-gray-500">Loading session...</div>;
  if (error) return <div className="text-red-600">Failed to load session: {error}</div>;
  if (!data || !data.session) return <div className="text-red-500">Session not found</div>;

  const session = data.session;
  const reports: Report[] = data.reports ?? [];
  const decisions: Decision[] = data.decisions ?? [];
  const progress = data.progress ?? { completed: reports.length, total: reports.length };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="bg-white rounded-lg border p-6">
        <div className="flex justify-between items-start">
          <div>
            <h1 className="text-xl font-bold text-gray-900">
              PR #{session.pr_number}: {session.pr_title}
            </h1>
            <div className="text-sm text-gray-500 mt-1">
              {session.repository_full_name} · {session.pr_author} ·{" "}
              {session.head_branch} → {session.base_branch}
            </div>
            <div className="text-xs text-gray-400 mt-1">
              Session: {session.id} · Commit: {session.head_sha?.slice(0, 7)}
            </div>
          </div>
          <div className="flex items-center gap-3">
            {session.status === "completed" && decisions.length === 0 && (
              <button
                onClick={() => setShowApproval(true)}
                className="px-4 py-2 bg-blue-600 text-white text-sm rounded-lg hover:bg-blue-700"
              >
                Make Decision
              </button>
            )}
            <VerdictBadge verdict={session.overall_verdict} large />
          </div>
        </div>

        {/* Progress bar */}
        {session.status === "running" && (
          <div className="mt-4">
            <div className="flex justify-between text-xs text-gray-500 mb-1">
              <span>Review in progress</span>
              <span>{progress.completed}/{progress.total} agents</span>
            </div>
            <div className="h-2 bg-gray-200 rounded-full">
              <div
                className="h-2 bg-blue-500 rounded-full transition-all"
                style={{ width: `${(progress.completed / Math.max(progress.total, 1)) * 100}%` }}
              />
            </div>
          </div>
        )}
      </div>

      {/* Approval Dialog */}
      {showApproval && (
        <div className="bg-white rounded-lg border-2 border-blue-200 p-6">
          <h2 className="font-bold text-lg mb-4">Submit Decision</h2>
          <div className="flex gap-3 mb-4">
            {(["approve", "request_changes", "defer"] as const).map((d) => (
              <button
                key={d}
                onClick={() => setDecision(d)}
                className={`px-4 py-2 rounded-lg text-sm border ${
                  decision === d
                    ? d === "approve" ? "bg-green-50 border-green-500 text-green-700"
                    : d === "request_changes" ? "bg-red-50 border-red-500 text-red-700"
                    : "bg-yellow-50 border-yellow-500 text-yellow-700"
                    : "border-gray-200 text-gray-600 hover:bg-gray-50"
                }`}
              >
                {d === "approve" ? "✅ Approve" : d === "request_changes" ? "🔄 Request Changes" : "⏸️ Defer"}
              </button>
            ))}
          </div>
          <textarea
            value={justification}
            onChange={(e) => setJustification(e.target.value)}
            placeholder="Justification (required for overrides, optional otherwise)"
            className="w-full border rounded-lg p-3 text-sm mb-4"
            rows={3}
          />
          <div className="flex gap-3">
            <button
              onClick={submitDecision}
              disabled={submitting}
              className="px-4 py-2 bg-blue-600 text-white text-sm rounded-lg hover:bg-blue-700 disabled:opacity-50"
            >
              {submitting ? "Submitting..." : "Submit Decision"}
            </button>
            <button
              onClick={() => setShowApproval(false)}
              className="px-4 py-2 text-sm text-gray-600 hover:text-gray-900"
            >
              Cancel
            </button>
          </div>
        </div>
      )}

      {/* Decisions */}
      {decisions.length > 0 && (
        <div className="bg-white rounded-lg border p-6">
          <h2 className="font-bold text-lg mb-3">Decisions</h2>
          {decisions.map((d: Decision) => (
            <div key={d.id} className="flex items-start gap-3 py-2 border-b last:border-0">
              <span className="text-lg">
                {d.decision === "approve" ? "✅" : d.decision === "request_changes" ? "🔄" : "⏸️"}
              </span>
              <div>
                <div className="text-sm font-medium">
                  {d.engineer_name} — {d.decision.replace("_", " ")}
                </div>
                <div className="text-xs text-gray-500">{new Date(d.decided_at).toLocaleString()}</div>
                {d.justification && (
                  <div className="text-sm text-gray-600 mt-1 italic">"{d.justification}"</div>
                )}
                {d.overridden_verdicts?.length > 0 && (
                  <div className="text-xs text-orange-600 mt-1">
                    ⚠️ {d.overridden_verdicts.length} verdict(s) overridden
                  </div>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Agent Reports */}
      <div className="space-y-4">
        <h2 className="font-bold text-lg">Agent Reports ({reports.length})</h2>
        {reports.length === 0 && session.status === "completed" && (
          <div className="bg-white rounded-lg border p-6 text-sm text-gray-400">
            No agent reports. This session finished without running any agents —
            check that at least one agent is enabled.
          </div>
        )}
        {reports.map((r: Report) => (
          <details key={r.id} className="bg-white rounded-lg border">
            <summary className="px-6 py-4 cursor-pointer flex items-center justify-between hover:bg-gray-50">
              <div className="flex items-center gap-3">
                <VerdictBadge verdict={r.verdict} />
                <span className="font-medium">{formatSkillName(r.skill_slug)}</span>
                <span className="text-xs text-gray-400">
                  {r.model_used} · {(r.latency_ms / 1000).toFixed(1)}s
                  {r.cost_estimate_usd ? ` · $${r.cost_estimate_usd.toFixed(4)}` : ""}
                </span>
              </div>
              <span className="text-xs text-gray-400">
                {r.findings?.length ?? 0} findings
              </span>
            </summary>
            <div className="px-6 pb-4 border-t">
              <pre className="mt-4 text-sm text-gray-700 whitespace-pre-wrap font-sans">
                {r.report_md}
              </pre>
            </div>
          </details>
        ))}
      </div>
    </div>
  );
}

function VerdictBadge({ verdict, large }: { verdict: string | null; large?: boolean }) {
  const map: Record<string, { bg: string; label: string }> = {
    pass: { bg: "bg-green-100 text-green-800", label: "✅ PASS" },
    warn: { bg: "bg-yellow-100 text-yellow-800", label: "⚠️ WARN" },
    fail: { bg: "bg-red-100 text-red-800", label: "🔴 FAIL" },
    error: { bg: "bg-red-100 text-red-800", label: "❌ ERROR" },
    not_applicable: { bg: "bg-gray-100 text-gray-600", label: "⏭️ N/A" },
  };
  const v = verdict ? map[verdict] : null;
  if (!v) return null;
  return (
    <span className={`${v.bg} ${large ? "px-3 py-1 text-sm" : "px-2 py-0.5 text-xs"} rounded-full font-medium`}>
      {v.label}
    </span>
  );
}

function formatSkillName(slug: string): string {
  return slug.split("-").map((w) => w.charAt(0).toUpperCase() + w.slice(1)).join(" ");
}
