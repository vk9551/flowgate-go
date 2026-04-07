export interface Stats {
  total_today: number
  act_now: number
  delayed: number
  suppressed: number
  suppression_rate: number
  avg_delay_seconds: number
  outcome_counts: Record<string, number>
  delivery_success_rate: number
}

export interface EventRecord {
  id: string
  subject_id: string
  priority: string
  decision: 'ACT_NOW' | 'DELAY' | 'SUPPRESS'
  reason: string
  occurred_at: string
  deliver_at?: string
  outcome?: string
  outcome_reason?: string
  resolved_at?: string
}

export interface Subject {
  id: string
  timezone: string
  updated_at: string
  channel_health?: Record<string, string>
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
