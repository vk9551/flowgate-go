import { useState } from 'react'
import { api } from '../api'
import type { SubjectDetail, EventRecord } from '../types'

function decisionClass(d: string) {
  if (d === 'SEND_NOW') return 'decision-send'
  if (d === 'DELAY') return 'decision-delay'
  return 'decision-suppress'
}

function fmt(iso: string) {
  try { return new Date(iso).toLocaleString() } catch { return iso }
}

export function Subjects() {
  const [query, setQuery] = useState('')
  const [result, setResult] = useState<SubjectDetail | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const lookup = async () => {
    const id = query.trim()
    if (!id) return
    setLoading(true)
    setError('')
    setResult(null)
    try {
      const r = await api.subject(id)
      setResult(r)
    } catch (e) {
      setError(String(e))
    } finally {
      setLoading(false)
    }
  }

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') lookup()
  }

  return (
    <div>
      <div className="section-header"><span>Subject lookup</span></div>
      <div className="search-row">
        <input
          className="search-input"
          placeholder="Enter subject ID…"
          value={query}
          onChange={e => setQuery(e.target.value)}
          onKeyDown={onKey}
        />
        <button className="btn" onClick={lookup} disabled={loading}>
          {loading ? '…' : 'Look up'}
        </button>
      </div>

      {error && <div className="error">{error}</div>}

      {result && (
        <div className="subject-result">
          <div className="subject-meta">
            <div><span className="meta-key">ID</span><span className="mono">{result.subject.id}</span></div>
            <div><span className="meta-key">Timezone</span><span>{result.subject.timezone || '—'}</span></div>
            <div><span className="meta-key">Last seen</span><span>{fmt(result.subject.updated_at)}</span></div>
          </div>

          {result.subject.channel_health && Object.keys(result.subject.channel_health).length > 0 && (
            <div className="channel-health">
              <div className="sub-section-title">Channel health</div>
              <div className="channel-badges">
                {Object.entries(result.subject.channel_health).map(([ch, outcome]) => (
                  <span key={ch} className={`channel-badge channel-badge-${outcome.replace('_', '-')}`}>
                    {ch}: {outcome}
                  </span>
                ))}
              </div>
            </div>
          )}

          <div className="sub-section-title">Event history (last 20)</div>
          {result.history.length === 0 ? (
            <div className="empty">No events recorded.</div>
          ) : (
            <div className="table-wrap">
              <table className="data-table">
                <thead>
                  <tr><th>Time</th><th>Priority</th><th>Decision</th><th>Reason</th></tr>
                </thead>
                <tbody>
                  {result.history.map((e: EventRecord) => (
                    <tr key={e.id} className={decisionClass(e.decision)}>
                      <td className="mono">{fmt(e.occurred_at)}</td>
                      <td>{e.priority}</td>
                      <td><span className={`badge badge-${e.decision.toLowerCase().replace('_', '-')}`}>{e.decision}</span></td>
                      <td className="reason">{e.reason}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
