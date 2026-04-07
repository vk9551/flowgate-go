import { useState } from 'react'
import { Overview } from './components/Overview'
import { Events } from './components/Events'
import { Subjects } from './components/Subjects'
import { Policies } from './components/Policies'
import { getToken, setToken } from './api'
import './App.css'

type Tab = 'overview' | 'events' | 'subjects' | 'policies'

const TABS: { id: Tab; label: string }[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'events', label: 'Events' },
  { id: 'subjects', label: 'Subjects' },
  { id: 'policies', label: 'Policies' },
]

export default function App() {
  const [tab, setTab] = useState<Tab>('overview')
  const [token, setTokenState] = useState(getToken)
  const [showToken, setShowToken] = useState(false)

  const saveToken = (t: string) => {
    setToken(t)
    setTokenState(t)
  }

  return (
    <div className="app">
      <header className="header">
        <div className="header-brand">
          <span className="brand-name">FlowGate</span>
          <span className="brand-tag">traffic governance</span>
        </div>
        <nav className="tabs">
          {TABS.map(t => (
            <button
              key={t.id}
              className={`tab-btn${tab === t.id ? ' active' : ''}`}
              onClick={() => setTab(t.id)}
            >
              {t.label}
            </button>
          ))}
        </nav>
        <button
          className="btn btn-sm token-btn"
          onClick={() => setShowToken(v => !v)}
          title="Configure API token"
        >
          {token ? 'token set' : 'no token'}
        </button>
      </header>

      {showToken && (
        <div className="token-bar">
          <input
            className="token-input"
            type="password"
            placeholder="Bearer token (leave empty if auth is disabled)"
            defaultValue={token}
            onBlur={e => saveToken(e.target.value)}
          />
          <button className="btn btn-sm" onClick={() => setShowToken(false)}>Close</button>
        </div>
      )}

      <main className="main">
        {tab === 'overview' && <Overview />}
        {tab === 'events' && <Events />}
        {tab === 'subjects' && <Subjects />}
        {tab === 'policies' && <Policies />}
      </main>
    </div>
  )
}
