import { useEffect, useState } from 'react'
import { api } from '../api'
import type { EventRecord } from '../types'

function decisionClass(d: string) {
  if (d === 'SEND_NOW') return 'decision-send'
  if (d === 'DELAY') return 'decision-delay'
  return 'decision-suppress'
}

function fmt(iso: string) {
  return new Date(iso).toLocaleTimeString()
}

export function Events() {
  const [events, setEvents] = useState<EventRecord[]>([])
  const [error, setError] = useState('')
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)

  const load = () =>
    api.eventsRecent(50)
      .then(e => { setEvents(e); setError(''); setLastUpdated(new Date()) })
      .catch(e => setError(String(e)))

  useEffect(() => {
    load()
    const id = setInterval(load, 10_000)
    return () => clearInterval(id)
  }, [])

  if (error) return <div className="error">{error}</div>

  return (
    <div>
      <div className="section-header">
        <span>Recent decisions (last 50)</span>
        {lastUpdated && <span className="last-updated">updated {lastUpdated.toLocaleTimeString()}</span>}
      </div>
      {events.length === 0 && !error ? (
        <div className="empty">No events yet.</div>
      ) : (
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>Time</th>
                <th>Subject</th>
                <th>Priority</th>
                <th>Decision</th>
                <th>Reason</th>
                <th>Deliver at</th>
              </tr>
            </thead>
            <tbody>
              {events.map(e => (
                <tr key={e.id} className={decisionClass(e.decision)}>
                  <td className="mono">{fmt(e.occurred_at)}</td>
                  <td className="mono">{e.subject_id}</td>
                  <td>{e.priority}</td>
                  <td><span className={`badge badge-${e.decision.toLowerCase().replace('_', '-')}`}>{e.decision}</span></td>
                  <td className="reason">{e.reason}</td>
                  <td className="mono">{e.deliver_at ? fmt(e.deliver_at) : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
