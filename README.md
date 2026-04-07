# FlowGate

**A self-hosted traffic governance engine. You POST events. It returns a decision: send now, delay until waking hours, or suppress.**

FlowGate never sends anything. It decides. You send.

---

## What it does

- **Routes traffic by priority** — OTPs pass through immediately; marketing emails respect quiet hours and daily caps
- **Enforces rate limits and waking-hour windows** — per-subject, rolling-window caps; delays outside configurable hours rather than dropping
- **Returns synchronous decisions** — your service gets `SEND_NOW`, `DELAY <timestamp>`, or `SUPPRESS` in one HTTP call; no polling

## What it doesn't do

FlowGate has no concept of email, SMS, push, or webhooks. It doesn't know what a notification is. It knows subjects, events, priorities, and policies. The calling system sends. FlowGate decides.

No cloud dependencies. No external queue. No database server. Runs on a $5 VPS.

---

## Quickstart

```bash
# 1. Clone
git clone https://github.com/flowgate/flowgate
cd flowgate

# 2. Create config (edit to taste)
cp config/flowgate.example.yaml flowgate.yaml
export FLOWGATE_SECRET=dev-secret-change-me

# 3. Start
docker compose up -d

# 4. Submit an event
curl -s -X POST http://localhost:7700/v1/events \
  -H "Authorization: Bearer $(jwt-cli encode --secret dev-secret-change-me '{}')" \
  -H "Content-Type: application/json" \
  -d '{"user_id":"user_123","type":"marketing_weekly","user_tz":"America/New_York"}'
```

Response:
```json
{
  "event_id": "3f1a2b4c-...",
  "decision": "SEND_NOW",
  "reason": "send_now",
  "priority": "bulk",
  "suppressed_today": 0
}
```

Send it again twice more. The third response:
```json
{
  "event_id": "...",
  "decision": "SUPPRESS",
  "reason": "cap_breached",
  "priority": "bulk",
  "suppressed_today": 1
}
```

**If you don't have a JWT library handy**, set `server.auth.type: none` in `flowgate.yaml` and drop the `Authorization` header.

---

## How it works

Every event goes through three stages:

**1. Priority matching** — event fields are matched against YAML rules (exact value, prefix, suffix, list membership, field existence). First match wins. A `default: true` priority catches everything else.

**2. Policy evaluation** — the matched priority's policy is applied:
- `bypass_all: true` → straight to `SEND_NOW`, no further checks
- Cap check → count events for this subject+priority in the rolling window; breach → `decision_on_cap_breach`
- Waking-hours check → if outside the subject's configured hours, `DELAY` with a `deliver_at` timestamp

**3. Decision** — one of:
| Decision | Meaning |
|----------|---------|
| `SEND_NOW` | Deliver immediately |
| `DELAY` | Hold until `deliver_at`; FlowGate calls your webhook when ready |
| `SUPPRESS` | Drop; reason code included |

FlowGate writes every decision to its event log (feeds the dashboard and caps).

---

## Configuration reference

All behaviour is in `flowgate.yaml`. Hot-reload via `POST /v1/policies/reload` — no restart needed.

```yaml
version: "1.0"

subject:
  id_field: "user_id"          # field in the event that identifies the subject
  timezone_field: "user_tz"    # IANA timezone field for waking-hours math
  waking_hours:
    start: "07:00"
    end:   "22:00"

priorities:
  - name: critical
    match:
      - field: "type"
        in: ["otp", "order_confirmed"]   # list membership
    bypass_all: true                     # skips all caps and waking hours

  - name: bulk
    match:
      - field: "type"
        prefix: "marketing_"             # prefix match
    default: true                        # catches unmatched events

policies:
  - priority: critical
    decision: send_now

  - priority: bulk
    window:
      respect_waking_hours: true
      max_delay: 48h                     # give up after 48h
    caps:
      - scope: subject
        period: 1d                       # rolling 24-hour window
        limit: 2
    decision_on_cap_breach: suppress
    digest:
      enabled: true
      wait: 4h                           # batch suppressed into one callback
      max_items: 10

callbacks:
  send_now:
    url:    "https://your-system.com/hooks/send"
    method: POST
    retries: 3
  suppressed:
    url:    "https://your-system.com/hooks/suppressed"
    method: POST
    include_reason: true
  digest_ready:
    url:    "https://your-system.com/hooks/digest"
    method: POST

storage:
  backend: sqlite
  dsn:     /data/flowgate.db    # path inside container

server:
  port: 7700
  auth:
    type:   jwt                  # jwt | none
    secret: "${FLOWGATE_SECRET}" # env var expansion supported
  dashboard:
    enabled: true
```

See [`config/flowgate.example.yaml`](config/flowgate.example.yaml) for three complete examples: notification throttling, API rate shaping, and webhook deduplication.

---

## API reference

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/v1/events` | ✓ | Submit event, get decision |
| `GET` | `/v1/subjects/:id` | ✓ | Subject profile + last 20 events |
| `DELETE` | `/v1/subjects/:id` | ✓ | Reset subject caps and history |
| `GET` | `/v1/policies` | ✓ | Dump current loaded config |
| `POST` | `/v1/policies/reload` | ✓ | Hot-reload config from disk |
| `GET` | `/v1/stats` | ✓ | Today's decision counts |
| `GET` | `/v1/events/recent` | ✓ | Last N decisions (default 50) |
| `GET` | `/v1/health` | — | Liveness check |
| `GET` | `/dashboard` | — | Embedded React dashboard |

### POST /v1/events

Request body is a flat JSON object. Any field can be used in match rules.

```json
{
  "user_id": "user_123",
  "type":    "marketing_weekly",
  "user_tz": "America/New_York"
}
```

Response:
```json
{
  "event_id":        "3f1a2b4c-...",
  "decision":        "DELAY",
  "deliver_at":      "2026-04-07T07:00:00Z",
  "reason":          "quiet_hours",
  "priority":        "bulk",
  "suppressed_today": 0
}
```

---

## Use cases

### 1. Notification throttling

You run a SaaS and send transactional + marketing emails. Users unsubscribe because they get too many.

Put FlowGate in front of your email queue. OTPs always send. Marketing is capped at 2/day per user, delayed to waking hours, and batched into a digest when suppressed. Users stop unsubscribing.

→ See `# EXAMPLE 1` in `config/flowgate.example.yaml`

### 2. API rate shaping

Your service fans out to downstream APIs with different rate limits. Realtime calls always go through. Standard calls are capped per client per minute. Batch jobs are throttled to a daily budget.

FlowGate sits in front of your outbound client pool. Calls that breach the cap are delayed briefly instead of erroring.

→ See `# EXAMPLE 2` in `config/flowgate.example.yaml`

### 3. Webhook deduplication

Your upstream fires duplicate webhooks within seconds of each other. Your processing is idempotent but the work is expensive.

Point your webhook endpoint at FlowGate first. Same source ID within a 5-minute window → `SUPPRESS`. Only the first fires through.

→ See `# EXAMPLE 3` in `config/flowgate.example.yaml`

---

## Why FlowGate

This came from running 2 billion notifications per month.

The engineering was fine. The deliverability was fine. The problem was simpler: we were sending too much, too often, at the wrong times. Users didn't hate the product — they hated the volume. Unsubscribes and notification-off rates climbed quarter over quarter, and every new feature that sent a notification made it worse.

The fix required logic that wasn't in any single service. The email service didn't know about the push service. The push service didn't know the user was asleep. The marketing team's batch jobs didn't know about transactional messages that fired ten minutes earlier.

We needed a single place to enforce the rule: *before you send anything to anyone, ask someone who knows the full picture*.

FlowGate is that place. It's domain-agnostic by design — it doesn't know what email or push means, so it can't accumulate assumptions. You define what matters (your priorities), what's acceptable (your caps), and what to respect (your users' waking hours). FlowGate enforces it.

---

## Self-hosting

### Docker (recommended)

```bash
cp config/flowgate.example.yaml flowgate.yaml
# Edit flowgate.yaml — set your callback URLs, caps, priorities.

export FLOWGATE_SECRET=$(openssl rand -hex 32)
docker compose up -d

# Logs
docker compose logs -f

# Reload config without restarting
curl -X POST http://localhost:7700/v1/policies/reload \
  -H "Authorization: Bearer $TOKEN"
```

The SQLite database is written to `./data/flowgate.db` on the host.

### Single binary

```bash
make build
./bin/flowgate flowgate.yaml
```

### Mac + Colima

```bash
colima start
docker compose up -d
```

The Colima socket is used automatically by Docker Compose v2.

### Storage backends

| Backend | When to use |
|---------|------------|
| SQLite (default) | Single instance, up to ~10k events/day |
| Redis | Multi-instance, high throughput (coming in a future session) |
| Postgres | Audit requirements, complex queries (coming in a future session) |

---

## Makefile

```
make build    # npm run build + go build → bin/flowgate
make test     # go test ./...
make dev      # Go server (:7700) + Vite dev server (:5173) concurrently
make clean    # remove bin/ and dist/
```

---

## Contributing

1. Fork and clone
2. `make dev` to run locally
3. Tests: `make test`
4. Open a PR — include what you changed and why

Issues tracked at [github.com/flowgate/flowgate/issues](https://github.com/flowgate/flowgate/issues).

---

## License

MIT — see [LICENSE](LICENSE).
