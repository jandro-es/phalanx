// =============================================================================
// Phalanx Dashboard — Provider Configuration
// =============================================================================

import React, { useEffect, useState } from "react";
import { useApi } from "../App";

export function ProviderConfig() {
  const api = useApi();
  const [providers, setProviders] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    api.get("/api/providers").then((d) => { setProviders(d.providers); setLoading(false); });
  }, []);

  if (loading) return <div className="text-gray-500">Loading providers...</div>;

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900 mb-6">LLM Providers</h1>
      <div className="grid gap-4">
        {providers.map((p) => (
          <div key={p.id} className="bg-white rounded-lg border p-6">
            <div className="flex justify-between items-start">
              <div>
                <h3 className="font-bold text-lg">{p.name}</h3>
                <div className="text-sm text-gray-500 mt-1">{p.base_url}</div>
              </div>
              <span className="bg-green-100 text-green-700 text-xs px-2 py-1 rounded">
                Active
              </span>
            </div>
            <div className="mt-4 grid grid-cols-3 gap-4 text-sm">
              <div>
                <div className="text-gray-400 text-xs">Default Model</div>
                <div className="font-mono text-gray-700">{p.default_model}</div>
              </div>
              <div>
                <div className="text-gray-400 text-xs">Auth Method</div>
                <div className="text-gray-700">{p.auth_method}</div>
              </div>
              <div>
                <div className="text-gray-400 text-xs">Available Models</div>
                <div className="flex flex-wrap gap-1 mt-1">
                  {(p.models ?? []).map((m: string) => (
                    <span key={m} className="bg-gray-100 text-gray-600 text-xs px-2 py-0.5 rounded">
                      {m}
                    </span>
                  ))}
                </div>
              </div>
            </div>
            {p.config && Object.keys(p.config).length > 0 && (
              <details className="mt-4">
                <summary className="text-xs text-gray-400 cursor-pointer">Configuration</summary>
                <pre className="mt-2 text-xs bg-gray-50 p-2 rounded">
                  {JSON.stringify(p.config, null, 2)}
                </pre>
              </details>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
