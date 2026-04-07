export interface Stats {
  total_today: number
  send_now: number
  delayed: number
  suppressed: number
  suppression_rate: number
  avg_delay_seconds: number
  active_scheduled: number
}

export interface EventRecord {
  id: string
  subject_id: string
  priority: string
  decision: 'SEND_NOW' | 'DELAY' | 'SUPPRESS'
  reason: string
  occurred_at: string
  deliver_at?: string
}

export interface Subject {
  id: string
  timezone: string
  updated_at: string
}

export interface SubjectDetail {
  subject: Subject
  history: EventRecord[]
}

export interface Policy {
  priority: string
  decision?: string
  window?: {
    respect_waking_hours?: boolean
    max_delay?: string
  }
  caps?: Array<{
    scope: string
    period: string
    limit: number
  }>
  decision_on_cap_breach?: string
}

export interface Config {
  version: string
  priorities: Array<{
    name: string
    bypass_all?: boolean
    default?: boolean
  }>
  policies: Policy[]
}
