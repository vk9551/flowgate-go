import { useEffect, useState } from 'react'
import { api } from '../api'
import type { Stats } from '../types'

function StatCard({ label, value, sub }: { label: string; value: string | number; sub?: string }) {
  return (
    <div className="stat-card">
      <div className="stat-value">{value}</div>
      <div className="stat-label">{label}</div>
      {sub && <div className="stat-sub">{sub}</div>}
    </div>
  )
}

export function Overview() {
  const [stats, setStats] = useState<Stats | null>(null)
  const [error, setError] = useState('')
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)

  const load = () =>
    api.stats()
      .then(s => { setStats(s); setError(''); setLastUpdated(new Date()) })
      .catch(e => setError(String(e)))

  useEffect(() => {
    load()
    const id = setInterval(load, 10_000)
    return () => clearInterval(id)
  }, [])

  if (error) return <div className="error">{error}</div>
  if (!stats) return <div className="loading">Loading…</div>

  const avgDelay = stats.avg_delay_seconds > 0
    ? `${Math.round(stats.avg_delay_seconds / 60)}m avg`
    : '—'

  return (
    <div>
      <div className="section-header">
        <span>Today's decisions</span>
        {lastUpdated && <span className="last-updated">updated {lastUpdated.toLocaleTimeString()}</span>}
      </div>
      <div className="stat-grid">
        <StatCard label="Total" value={stats.total_today} />
        <StatCard label="Send now" value={stats.send_now} />
        <StatCard label="Delayed" value={stats.delayed} sub={avgDelay} />
        <StatCard label="Suppressed" value={stats.suppressed} sub={`${stats.suppression_rate.toFixed(1)}%`} />
        <StatCard label="Active scheduled" value={stats.active_scheduled} />
        <StatCard label="Suppression rate" value={`${stats.suppression_rate.toFixed(1)}%`} />
      </div>
    </div>
  )
}
