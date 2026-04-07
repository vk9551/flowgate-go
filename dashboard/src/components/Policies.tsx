import { useEffect, useState } from 'react'
import { api } from '../api'
import type { Config } from '../types'

export function Policies() {
  const [config, setConfig] = useState<Config | null>(null)
  const [error, setError] = useState('')
  const [reloadMsg, setReloadMsg] = useState('')

  const load = () =>
    api.policies()
      .then(c => { setConfig(c); setError('') })
      .catch(e => setError(String(e)))

  useEffect(() => { load() }, [])

  const reload = async () => {
    setReloadMsg('')
    try {
      await api.reloadPolicies()
      setReloadMsg('✓ Policy reloaded')
      load()
    } catch (e) {
      setReloadMsg(`✗ ${String(e)}`)
    }
    setTimeout(() => setReloadMsg(''), 3000)
  }

  if (error) return <div className="error">{error}</div>
  if (!config) return <div className="loading">Loading…</div>

  return (
    <div>
      <div className="section-header">
        <span>Current policy</span>
        <div className="header-actions">
          {reloadMsg && <span className="reload-msg">{reloadMsg}</span>}
          <button className="btn" onClick={reload}>Reload policy</button>
        </div>
      </div>
      <pre className="json-viewer">{JSON.stringify(config, null, 2)}</pre>
    </div>
  )
}
