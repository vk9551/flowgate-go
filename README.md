# FlowGate

**A self-hosted traffic governance engine. You POST events. It returns one of three decisions: act now, act later, or drop.**

FlowGate never executes anything. It decides. You act.

---

## What it does

- **Routes by priority** — high-urgency events pass through immediately; lower-priority ones respect active-window constraints and rolling caps
- **Enforces rate limits and active-window policies** — per-subject, rolling-window caps; delays outside configurable windows rather than dropping
- **Returns synchronous decisions** — your service gets `ACT_NOW`, `DELAY <timestamp>`, or `SUPPRESS` in one HTTP call; no polling

## What it doesn't do

FlowGate has no concept of what the action is. It knows subjects, events, priorities, and policies — nothing else. It does not know or care what happens after the decision. The caller executes. FlowGate decides.

No cloud dependencies. No external queue. No database server. Runs on a $5 VPS.

---

## Quickstart

FlowGate exposes two ports:
- **7700** — REST API + dashboard
- **7701** — gRPC (enable with `grpc_port: 7701` in `flowgate.yaml`)

```bash
# 1. Clone
git clone https://github.com/flowgate/flowgate
cd flowgate

# 2. Create config (edit to taste)
cp config/flowgate.example.yaml flowgate.yaml
export FLOWGATE_SECRET=dev-secret-change-me

# 3. Start  (Mac + Colima: run `colima start` first)
docker-compose up -d

# 4. Generate a token and submit an event
TOKEN=$(python3 -c "
import json, base64, hmac, hashlib, time
h = base64.urlsafe_b64encode(json.dumps({'alg':'HS256','typ':'JWT'}).encode()).rstrip(b'=')
p = base64.urlsafe_b64encode(json.dumps({'sub':'me','exp':int(time.time())+86400}).encode()).rstrip(b'=')
sig = base64.urlsafe_b64encode(hmac.new(b'dev-secret-change-me', h+b'.'+p, hashlib.sha256).digest()).rstrip(b'=')
print((h+b'.'+p+b'.'+sig).decode())
")

curl -s -X POST http://localhost:7700/v1/events \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"user_id":"user_123","urgency":"low","action":"batch_report","user_tz":"America/New_York"}'
```

Response:
```json
{
  "event_id": "3f1a2b4c-...",
  "decision": "ACT_NOW",
  "reason": "act_now",
  "priority": "bulk",
  "suppressed_today": 0
}
```

Submit the same event twice more. The third response:
```json
{
  "event_id": "...",
  "decision": "SUPPRESS",
  "reason": "cap_breached",
  "priority": "bulk",
  "suppressed_today": 1
}
```

**Skip auth entirely** — set `server.auth.type: none` in `flowgate.yaml` and drop the `Authorization` header.

---

## How it works

Every event goes through three stages:

**1. Priority matching** — event fields are matched against YAML rules (exact value, prefix, suffix, list membership, field existence). First match wins. A `default: true` priority catches everything else.

**2. Policy evaluation** — the matched priority's policy is applied:
- `bypass_all: true` → straight to `ACT_NOW`, no further checks
- Cap check → count events for this subject+priority in the rolling window; breach → `decision_on_cap_breach`
- Active-window check → if outside the subject's configured active hours, `DELAY` with a `deliver_at` timestamp

**3. Decision** — one of:

| Decision | Meaning |
|----------|---------|
| `ACT_NOW` | Act now |
| `DELAY` | Act later; `deliver_at` tells you when — your system handles scheduling |
| `SUPPRESS` | Drop; reason code included |

FlowGate writes every decision to its event log (feeds the dashboard and caps).

---

## Configuration reference

All behaviour is in `flowgate.yaml`. Hot-reload via `POST /v1/policies/reload` — no restart needed.

```yaml
version: "1.0"

subject:
  id_field: "user_id"          # field in the event that identifies the subject
  timezone_field: "user_tz"    # IANA timezone field for active-window math
  waking_hours:                # the subject's active window
    start: "07:00"
    end:   "22:00"

priorities:
  - name: critical
    match:
      - field: "urgency"
        equals: "critical"     # exact match
    bypass_all: true           # skips all caps and active-window checks

  - name: standard
    match:
      - field: "urgency"
        in: ["high", "normal"] # list membership

  - name: bulk
    match:
      - field: "urgency"
        equals: "low"          # exact match
    default: true              # catches unmatched events

policies:
  - priority: critical
    decision: act_now

  - priority: standard
    window:
      respect_waking_hours: true
      max_delay: 12h
    caps:
      - scope: subject
        period: 1d             # rolling 24-hour window
        limit: 10
    decision_on_cap_breach: suppress

  - priority: bulk
    window:
      respect_waking_hours: true
      max_delay: 48h           # give up after 48h
    caps:
      - scope: subject
        period: 1d
        limit: 2
    decision_on_cap_breach: suppress

storage:
  backend: sqlite
  dsn:     /data/flowgate.db   # path inside container

server:
  port: 7700
  auth:
    type:   jwt                # jwt | none
    secret: "${FLOWGATE_SECRET}"
  dashboard:
    enabled: true
```

See [`config/flowgate.example.yaml`](config/flowgate.example.yaml) for three complete examples.

---

## Token generation

FlowGate uses HS256 JWT by default. You need a token for every authenticated request.

**Python (no dependencies):**
```bash
TOKEN=$(python3 -c "
import json, base64, hmac, hashlib, time
secret = b'your-secret-here'
h = base64.urlsafe_b64encode(json.dumps({'alg':'HS256','typ':'JWT'}).encode()).rstrip(b'=')
p = base64.urlsafe_b64encode(json.dumps({'sub':'me','exp':int(time.time())+86400}).encode()).rstrip(b'=')
sig = base64.urlsafe_b64encode(hmac.new(secret, h+b'.'+p, hashlib.sha256).digest()).rstrip(b'=')
print((h+b'.'+p+b'.'+sig).decode())
")
echo $TOKEN
```

**Node.js:**
```bash
# npm install -g jsonwebtoken  (once)
node -e "console.log(require('jsonwebtoken').sign({sub:'me'}, 'your-secret-here', {expiresIn:'24h'}))"
```

**Go (in the project):**
```bash
go run - <<'EOF'
package main
import (
    "fmt"; "time"
    "github.com/golang-jwt/jwt/v5"
)
func main() {
    tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256,
        jwt.MapClaims{"sub":"me","exp":time.Now().Add(24*time.Hour).Unix()},
    ).SignedString([]byte("your-secret-here"))
    fmt.Println(tok)
}
EOF
```

Once you have a token, pass it as:
```
Authorization: Bearer <token>
```

Or paste it into the **token field** on the FlowGate dashboard.

---

## Dashboard

Open **http://localhost:7700/dashboard** in your browser.

On first load you'll see a token prompt at the top. Paste your JWT there — it's saved in `localStorage` so you only need to do it once per browser.

**Tabs:**

| Tab | What you see |
|-----|-------------|
| Overview | Today's decision counts, suppression rate, execution success rate (auto-refreshes every 10s) |
| Events | Last 50 decisions across all subjects, with outcome column (green = success, amber = failed_temp, red = failed_perm, gray = pending) |
| Subjects | Look up any subject by ID — event history + execution-path health badges |
| Policies | Live config dump + reload button |

---

## API reference

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/v1/events` | ✓ | Submit event, get decision |
| `POST` | `/v1/events/{event_id}/outcome` | ✓ | Report execution outcome |
| `GET` | `/v1/subjects/{id}` | ✓ | Subject profile + last 20 events + path health |
| `DELETE` | `/v1/subjects/{id}` | ✓ | Reset subject caps and history |
| `GET` | `/v1/policies` | ✓ | Dump current loaded config |
| `POST` | `/v1/policies/reload` | ✓ | Hot-reload config from disk |
| `GET` | `/v1/stats` | ✓ | Today's decisions + all-time outcome counts |
| `GET` | `/v1/events/recent` | ✓ | Last N decisions (default 50, max 500) |
| `GET` | `/v1/health` | — | Liveness check |
| `GET` | `/dashboard` | — | Embedded React dashboard |

### POST /v1/events

Request body is a flat JSON object. Any field can be used in match rules. FlowGate doesn't care what the fields represent.

```json
{
  "user_id":  "user_123",
  "urgency":  "normal",
  "action":   "batch_report",
  "user_tz":  "America/New_York"
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

### POST /v1/events/{event_id}/outcome

After your system attempts to act on an event, report back what happened. FlowGate uses this to:
- Refund the cap slot if execution failed (`failed_temp`, `failed_perm`)
- Track execution-path health per subject for permanent failures
- Drive the success rate stat on the dashboard

```bash
curl -X POST http://localhost:7700/v1/events/3f1a2b4c-.../outcome \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "outcome":  "failed_perm",
    "reason":   "target_unreachable",
    "metadata": {"channel": "primary"}
  }'
```

Response:
```json
{
  "event_id":         "3f1a2b4c-...",
  "outcome":          "failed_perm",
  "cap_refunded":     true,
  "previous_outcome": "pending"
}
```

**Built-in outcomes:**

| Outcome | Refunds cap | Terminal | Meaning |
|---------|-------------|----------|---------|
| `success` | No | Yes | Execution succeeded; cap slot consumed as intended |
| `failed_temp` | Yes | No | Transient failure; cap slot returned, can be retried |
| `failed_perm` | Yes | Yes | Permanent failure; cap returned, path marked unhealthy |
| `pending` | No | No | Default; set automatically when the event is first created |

`terminal = true` means no further outcome updates are accepted. Submitting the same terminal outcome again is a no-op. Submitting a *different* terminal outcome returns `409 Conflict`.

The optional `metadata.channel` field names which execution path was used (e.g. `"primary"`, `"fallback"`, `"worker-a"`). This label appears in the Subjects tab health display. If omitted, the event's priority name is used.

Custom outcomes can be defined in `flowgate.yaml`:
```yaml
outcomes:
  - name: rejected
    refund_cap: true
    terminal: true
  - name: queued
    refund_cap: false
    terminal: false

default_outcome: pending
```

### GET /v1/subjects/{id}

```json
{
  "subject": {
    "id": "user_123",
    "timezone": "America/New_York",
    "updated_at": "2026-04-07T15:07:41Z",
    "channel_health": {
      "primary":  "failed_perm",
      "fallback": "success"
    }
  },
  "history": [...]
}
```

`channel_health` is populated by outcome reports and shows the last recorded outcome per named execution path.

### GET /v1/stats

```json
{
  "total_today":    17,
  "act_now":        15,
  "delayed":         0,
  "suppressed":      2,
  "suppression_rate": 11.76,
  "avg_delay_seconds": 0,
  "outcome_counts": {
    "success":     3,
    "failed_temp": 0,
    "failed_perm": 1,
    "pending":     13
  },
  "delivery_success_rate": 75.0
}
```

`delivery_success_rate` = `success / (success + failed_temp + failed_perm) × 100`. Events still pending are excluded from the denominator.

---

## gRPC API

FlowGate also exposes a gRPC server on port 7701. The service contract is defined in [flowgate-proto](https://github.com/vk9551/flowgate-proto).

Enable in `flowgate.yaml`:

```yaml
server:
  port: 7700
  grpc_port: 7701
```

Test with grpcurl:

```bash
grpcurl -plaintext localhost:7701 flowgate.v1.FlowGate/Health
```

All REST endpoints have gRPC equivalents:

| RPC             | REST equivalent              |
|-----------------|------------------------------|
| Evaluate        | POST /v1/events              |
| ReportOutcome   | POST /v1/events/:id/outcome  |
| EvaluateStream  | — (streaming only via gRPC)  |
| GetSubject      | GET /v1/subjects/:id         |
| ResetSubject    | DELETE /v1/subjects/:id      |
| GetPolicies     | GET /v1/policies             |
| ReloadPolicies  | POST /v1/policies/reload     |
| GetStats        | GET /v1/stats                |
| GetRecentEvents | GET /v1/events/recent        |
| Health          | GET /v1/health               |

---

## Use cases

### 1. High-frequency action throttling

You have subjects triggering the same low-urgency action repeatedly. Without control, they overwhelm the downstream system.

Set a rolling cap per subject per day. High-urgency events bypass it entirely. Lower-priority ones are capped and delayed to the subject's active window. The downstream only processes what it can handle.

→ See `# EXAMPLE 1` in `config/flowgate.example.yaml`

### 2. Outbound rate shaping

Your service fans out to downstream systems that have their own rate limits. Realtime calls always go through. Standard calls are capped per subject per minute. Batch jobs are throttled to a daily budget.

FlowGate sits in front of your outbound calls. Events that breach a cap are delayed briefly rather than rejected or retried blindly.

→ See `# EXAMPLE 2` in `config/flowgate.example.yaml`

### 3. Duplicate suppression

Your upstream fires the same event multiple times in quick succession. Processing is idempotent but expensive.

Route events through FlowGate first. Same subject ID within a short rolling window → `SUPPRESS`. Only the first passes through.

→ See `# EXAMPLE 3` in `config/flowgate.example.yaml`

---

## Why FlowGate

The problem this solves isn't domain-specific.

Any system that takes actions on behalf of subjects — at volume, across multiple services, with varying urgency — eventually needs a single place to answer the question: *should I act right now, wait, or skip this entirely?*

Without that, each service makes the decision independently. One service doesn't know what another just did to the same subject. High-urgency and low-urgency events compete on the same path with no differentiation. Caps and timing constraints get duplicated across teams and drift apart over time.

FlowGate is that single decision point. It's intentionally domain-agnostic — it has no opinion about what the action is. You define what matters (priorities), what's acceptable (caps and windows), and what to do when limits are hit. FlowGate enforces it uniformly, across every caller, for every subject.

---

## Self-hosting

### Docker (recommended)

```bash
cp config/flowgate.example.yaml flowgate.yaml
# Edit flowgate.yaml — set your caps, priorities, and active-window settings.

export FLOWGATE_SECRET=$(openssl rand -hex 32)
docker-compose up -d

# Logs
docker-compose logs -f

# Reload config without restarting
curl -X POST http://localhost:7700/v1/policies/reload \
  -H "Authorization: Bearer $TOKEN"

# Stop
docker-compose down
```

The SQLite database is written to `./data/flowgate.db` on the host.

### Single binary

```bash
make build
FLOWGATE_SECRET=your-secret ./bin/flowgate flowgate.yaml
```

### Mac + Colima

Colima is a lightweight Docker runtime for macOS. Install once, then use `docker-compose` as normal.

```bash
# Install (once)
brew install colima docker docker-compose

# Start the VM (once per boot)
colima start

# Run FlowGate
cp config/flowgate.example.yaml flowgate.yaml
export FLOWGATE_SECRET=dev-secret-change-me
docker-compose up -d

# Open dashboard
open http://localhost:7700/dashboard
```

### Storage backends

| Backend | When to use |
|---------|------------|
| SQLite (default) | Single instance, up to ~10k events/day |
| Redis | Multi-instance, high throughput (planned) |
| Postgres | Audit requirements, complex queries (planned) |

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

---

## Part of the FlowGate project

| Implementation | Repo | Language |
|---------------|------|----------|
| flowgate-go     | github.com/vk9551/flowgate-go     | Go                   |
| flowgate-node   | github.com/vk9551/flowgate-node   | TypeScript / Node.js |
| flowgate-python | github.com/vk9551/flowgate-python | Python               |
| flowgate-java   | github.com/vk9551/flowgate-java   | Java                 |
| flowgate-proto  | github.com/vk9551/flowgate-proto  | Protobuf             |

The YAML policy format, API contract, and decision logic are identical across implementations. `flowgate.yaml` files are interchangeable.

---

## License

MIT — see [LICENSE](LICENSE).
