import type { Stats, EventRecord, SubjectDetail, Config } from './types'

const TOKEN_KEY = 'flowgate_token'

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) ?? ''
}

export function setToken(t: string) {
  localStorage.setItem(TOKEN_KEY, t)
}

function headers(): HeadersInit {
  const token = getToken()
  const h: Record<string, string> = { 'Content-Type': 'application/json' }
  if (token) h['Authorization'] = `Bearer ${token}`
  return h
}

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: headers() })
  if (!res.ok) throw new Error(`${path}: HTTP ${res.status}`)
  return res.json() as Promise<T>
}

async function post<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method: 'POST',
    headers: headers(),
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) throw new Error(`${path}: HTTP ${res.status}`)
  return res.json() as Promise<T>
}

export const api = {
  stats: () => get<Stats>('/v1/stats'),
  eventsRecent: (limit = 50) => get<EventRecord[]>(`/v1/events/recent?limit=${limit}`),
  subject: (id: string) => get<SubjectDetail>(`/v1/subjects/${encodeURIComponent(id)}`),
  policies: () => get<Config>('/v1/policies'),
  reloadPolicies: () => post<{ status: string }>('/v1/policies/reload'),
}
