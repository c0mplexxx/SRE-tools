# alert-list-bot

`alert-list-bot` is a single-instance Telegram polling bot for the explicit
non-zero tenant active-alert view on `alerts-primary`. It handles a small
command set from an allowlisted chat, queries local Alertmanager, keeps alerts
whose `labels.tenant` is present and not `0`, and replies in the same chat.
Short-lived notification alerts with `labels.kind=notify` stay out of this
active incident list.

## Build and test

```bash
go test ./...
go test -race ./...
go vet ./...
go build -o alert-list-bot ./cmd/alert-list-bot
```

The service uses only the Go standard library. Build it on `alerts-primary`, or copy
a Linux binary built for the target architecture to `/usr/local/bin/alert-list-bot`.

## Debian package

The repository can build a local `.deb` package without external Go
dependencies. Run the package build on a Debian/Ubuntu builder with `dpkg-deb`:

```bash
cd bot
VERSION=1.3.1 packaging/deb/build.sh
```

If `VERSION` is omitted, the script uses the latest local
`alert-list-bot/v*` git tag.

The package is written to `dist/`, installs the binary to
`/usr/local/bin/alert-list-bot`, installs the systemd unit to
`/lib/systemd/system/alert-list-bot.service`, and creates
`/etc/alert-list-bot/alert-list-bot.env` from the example only when the real env
file does not already exist. The package does not enable, start, or restart the
service automatically; keep one Telegram polling instance active by enabling it
only on `alerts-primary`.

Install or upgrade:

```bash
sudo dpkg -i dist/alert-list-bot_1.3.1-1_amd64.deb
sudoedit /etc/alert-list-bot/alert-list-bot.env
sudo systemctl daemon-reload
sudo systemctl enable --now alert-list-bot.service
```

If a host still has a manually installed
`/etc/systemd/system/alert-list-bot.service`, systemd will prefer that file over
the packaged unit. Compare it with the packaged unit before removing the manual
override.

For an already running service, restart explicitly after upgrading:

```bash
sudo systemctl restart alert-list-bot.service
sudo systemctl status alert-list-bot.service
journalctl -u alert-list-bot.service -n 50 --no-pager
```

## Runtime config

Required config:

| Name | Meaning |
| --- | --- |
| `TELEGRAM_BOT_TOKEN` | Telegram Bot API token. Keep it out of the repo. |
| `TELEGRAM_ALLOWED_CHAT_IDS` | Comma-separated chat IDs that may use bot commands. |

Optional config:

| Name | Default |
| --- | --- |
| `ALERTMANAGER_URL` | `http://127.0.0.1:9093` |
| `VMALERT_URL_TENANT_1` | `http://127.0.0.1:8881` |
| `METRICS_URL_TENANT_1` | empty; required for `/check`, graph commands, and generic `/coverage` probes |
| `METRICS_URL_TENANT_0` | empty; reserved for tenant-0 commands such as future traffic checks |
| `HTTP_TIMEOUT` | `45s` |
| `TELEGRAM_POLL_TIMEOUT` | `30s` |
| `RETRY_DELAY` | `2s` |
| `TELEGRAM_MESSAGE_LIMIT` | `4096` |
| `TELEGRAM_EXPANDABLE_QUOTES` | `true` |

Every env value also has a flag form; run `alert-list-bot -h` for the exact flag
names. `TELEGRAM_API_BASE_URL` exists for tests and should normally keep the
Bot API default.

## Commands

```text
/?                              active non-zero tenant alerts
/id                             active non-zero tenant alerts with Alertmanager fingerprints
/status                         bot and Alertmanager readiness/counts
/silences                       active non-zero tenant silences
/check instance range           compact node_exporter metrics for one instance
/cpu instance range             CPU usage PNG graph for one tenant-1 instance
/mem instance range             memory usage PNG graph for one tenant-1 instance
/la instance range              load average PNG graph for one tenant-1 instance
/space instance range           top filesystem usage PNG graph for one tenant-1 instance
/swap instance range            swap usage PNG graph for one tenant-1 instance
/io instance range              top disk busy PNG graph for one tenant-1 instance
/rx instance range              top receive bit/s PNG graph for one tenant-1 instance
/tx instance range              top transmit bit/s PNG graph for one tenant-1 instance
/coverage instance              alert rule coverage for one instance
/silence alert-id duration      silence one current active alert by fingerprint
/silence label=value|label=~regex,... duration
                                silence non-zero tenant alerts by exact or regex labels
/ack alert-id                   silence one current active alert for 30m
/unsilence silence-id[,silence-id...]
                                expire active silences by id
deploy / деплой                 probabilistic non-mutating deploy joke
/help                           command help
```

`deploy` / `деплой` is a hidden lightweight code word, not a real deploy
command. Any allowlisted message containing `deploy` or `деплой` as a
standalone word has a 25% chance to reply with one random Russian
SRE/DevOps/AntiDDoS joke. Embedded words such as `redeploy`, `deployment`, or
`деплойчик` are ignored. The reply does not call Alertmanager or mutate
silences; when the probability gate does not pass, the message is ignored.

`/silence` accepts positive durations with `s`, `m`, `h`, `d`, or `month`.
Examples: `10s`, `10m`, `10h`, `10d`, `1month`. A month is treated as 30 days.
The alert id must come from the current `/id` view, which includes explicit
non-zero tenants. `/silence` also accepts comma-separated exact and regex label
matchers, for example `/silence instance=node-01,job=node_exporter 2h` or
`/silence instance=~^node-.* 2h`. If tenant is omitted, label-based silences keep
the existing tenant-1 default; explicit tenant matchers must target non-zero
tenants, and tenant regexes that can match `0` or an empty tenant are rejected.
`/ack` uses the same id resolution as `/silence alert-id duration` and creates a
30-minute exact-label silence.

`/check` is read-only and queries the Prometheus-compatible datasource from
`METRICS_URL_TENANT_1`, which should point at the tenant-1 datasource for the bot
instance, for example `http://vmselect.example.net/select/1/prometheus`.
`METRICS_URL` remains a backward-compatible fallback for tenant `1`, but new
config should use the explicit `METRICS_URL_TENANT_1` shape so tenant-specific
commands such as future `/traffic` can bind to `METRICS_URL_TENANT_0`. The
command accepts one node_exporter `instance` value and a range from `1m` through
`24h`:

```text
/check node-01 1h
```

It replies with a compact Telegram HTML quote containing `up`, CPU usage over
the range, CPU cores, load averages, memory usage, top filesystem usage, top
disk I/O busy devices, and top receive/transmit network rates.
Quote bodies with more than four rendered lines use Telegram expandable quotes
to keep chat output compact.

The graph commands are also read-only and tenant-1 only. They query the same
`METRICS_URL_TENANT_1` datasource directly and send a PNG through Telegram
`sendPhoto`; they do not call Grafana APIs and do not touch Alertmanager state.
Accepted ranges are `1m` through `4w` with `m`, `h`, `d`, or `w` suffixes. The
bot computes a bounded `query_range` step near 240 points, with a minimum step
of `15s`, to keep long-range requests predictable:

```text
/cpu node-01 1h
/space node-01 1d
/rx node-01 1w
```

Graph command behavior:

| Command | Series |
| --- | --- |
| `/cpu` | CPU used percent from `node_cpu_seconds_total`. |
| `/mem` | memory used percent from `MemAvailable / MemTotal`. |
| `/la` | `node_load1`, `node_load5`, and `node_load15`. |
| `/space` | top 3 filesystems by current usage, graphed by `mountpoint`. |
| `/swap` | swap used percent; hosts without swap return a text "no swap data" response. |
| `/io` | top 3 disk busy devices. |
| `/rx` | top 3 receive devices in bit/s. |
| `/tx` | top 3 transmit devices in bit/s. |

`/coverage` is read-only and shows the tenant-1 alertnames whose rules can
theoretically evaluate for one requested `instance`, even when those alerts are
not firing. It reads the tenant-1 rule catalog from:

```text
GET ${VMALERT_URL_TENANT_1}/api/v1/rules
```

Then it keeps only alerting rules, excludes `labels.kind=notify`, and deduplicates
the output by `alertname`. Static rules with `labels.instance` equal to the
requested instance are covered directly. Generic rules are covered only when a
small source-metric probe finds matching tenant-1 series in
`METRICS_URL_TENANT_1`; the command does not evaluate alert expressions and does
not query currently active Alertmanager alerts.

The bot does not keep a public alertname catalog. Generic coverage is inferred
from source metric families referenced by rule expressions, so ordinary new
rules are picked up automatically when they use already supported exporters.
Code changes are needed only when a new exporter or metric family needs a new
coverage probe.

```text
/coverage node-01
```

The reply starts with `coverage tenant 1 | node-01` and then lists covered
alertnames in sorted order, one per line. If no covered rule is found, the reply
is `covered alertnames: 0`.

## Alert flow

List commands query Alertmanager:

```text
GET http://127.0.0.1:9093/api/v2/alerts?active=true&silenced=false&inhibited=false
```

Tenant filtering for active alerts happens from the Alertmanager alert labels
after the API response is decoded. Alerts are included only when `tenant` is
present and not `0`; missing-tenant alerts are excluded. Alerts labeled
`kind=notify` are omitted from the reply so one-shot operational notifications
do not look like active incidents.

The mutating commands are intentionally narrow. `/silence alert-id duration`
and `/ack` re-read the active unsilenced non-zero tenant list, find one exact
Alertmanager fingerprint from `/id`, and post a silence with exact matchers for
every label on that selected alert:

```text
POST http://127.0.0.1:9093/api/v2/silences
```

`/silence label=value,... duration` creates a bounded label-based silence
without selecting a current alert id. It supports exact `label=value` and regex
`label=~regex` matchers, auto-adds `tenant=1` when tenant is omitted, rejects
negative matchers, and accepts only the bounded operator label set used by this stack:
`alertgroup`, `alertname`, `device`, `domain`, `instance`, `job`, `kind`,
`mountpoint`, `name`, `service`, `severity`, `tenant`, and `unit`. At least one
target label such as `instance`, `job`, `alertname`, `unit`, `name`, `service`,
`device`, or `mountpoint` is required. Regexes are compiled before Alertmanager
POST; target-label regexes that match an empty string are rejected.

`/silences` reads current silences with `GET /api/v2/silences`, keeps active
silences with an explicit tenant matcher other than `0`, and renders each
silence as an alert-like Telegram HTML block. Global silences and silences
without a tenant matcher are excluded from this view. It keeps only
operator-useful fields in the body: alert line, silence id, end time with
compact remaining duration, and short `silenced by`. Silence blocks are rendered as expandable quotes; `scrape_down` and
`systemd_down` silences without a `severity` matcher are still grouped as
`CRITICAL`. `/unsilence` accepts one silence id or comma-separated silence ids
and expires each one with:

```text
DELETE http://127.0.0.1:9093/api/v2/silence/silence-id
```

The chat cannot provide arbitrary silence matchers.

The `/check` command queries the tenant-1 datasource configured in
`METRICS_URL_TENANT_1`:

```text
GET ${METRICS_URL_TENANT_1}/api/v1/query
```

It does not call Alertmanager and does not mutate silences.

The graph commands query the same tenant-1 datasource with:

```text
GET ${METRICS_URL_TENANT_1}/api/v1/query
GET ${METRICS_URL_TENANT_1}/api/v1/query_range
```

Instant `query` is used only to choose top filesystems, disks, or network
devices for `/space`, `/io`, `/rx`, and `/tx`; PNG data comes from
`query_range`. Graph responses use Telegram `sendPhoto` multipart upload with
HTML captions.

The `/coverage` command queries the tenant-1 vmalert and metrics datasources
configured in `VMALERT_URL_TENANT_1` and `METRICS_URL_TENANT_1`:

```text
GET ${VMALERT_URL_TENANT_1}/api/v1/rules
GET ${METRICS_URL_TENANT_1}/api/v1/query
```

It does not call Alertmanager and does not mutate silences.

The reply order is deterministic:

1. Tenant `1` first with the current alert-block style.
2. Other explicit non-zero tenants below in their own tenant blocks.
3. `severity=critical` and `severity=high` under `CRITICAL`.
4. `severity=warning` under `WARNING`.
5. Any other firing alert under `OTHER`.

Each tenant and severity section header shows its active alert count and groups
bodies by `alertname`. Standard tenant-1 alerts use the current infra line contract:
systemd lines become
`DOWN | instance | unit`, `annotations.line` is preferred when present, and the
fallback uses `DOWN | instance | entity | alertname`.
DosGate CPU alerts with `alertgroup=dosgate-cpu-usage` render as
`WARN`/`HIGH`/`CRIT` from threshold `40`/`70`/`90` and reuse the current value
from their annotation line.

The reply uses Telegram HTML with escaped label and annotation values. When
`TELEGRAM_EXPANDABLE_QUOTES=true`, an `alertname` group with more than three
rows uses a collapsed quote body in Telegram. Large lists split only between
completed alert blocks.

## Deploy on alerts-primary

Create the service account and secret directory with the local policy you use
for service users. A typical shell-only-free setup is:

```bash
sudo useradd --system --home-dir /var/empty --shell /usr/sbin/nologin alert-list-bot
sudo install -d -m 0750 -o root -g alert-list-bot /etc/alert-list-bot
sudo install -m 0600 deploy/alert-list-bot.env.example /etc/alert-list-bot/alert-list-bot.env
```

Edit the environment file on `alerts-primary`, install the unit, then enable one
instance only:

```bash
sudo install -m 0644 deploy/alert-list-bot.service /etc/systemd/system/alert-list-bot.service
sudo systemctl daemon-reload
sudo systemctl enable --now alert-list-bot.service
sudo systemctl status alert-list-bot.service
```

Do not start another polling instance with the same bot token on `alerts-secondary`;
v1 relies on one `getUpdates` consumer.

## Smoke checks

Check the local Alertmanager view first:

```bash
curl -fsS 'http://127.0.0.1:9093/api/v2/alerts?active=true&silenced=false&inhibited=false'
journalctl -u alert-list-bot.service -f
```

Then send `/?`, `/id`, `/status`, `/silences`, `/check node-01 1h`,
`/cpu node-01 1h`, `/space node-01 1d`, `/coverage node-01`, `deploy`, `деплой`, or
`/help` from an allowlisted Telegram chat. Use an id from `/id` with
`/silence alert-id duration` or `/ack alert-id` only when one current
expendable alert should stop notifying. Use
`/silence instance=node-01,job=node_exporter 10m` or
`/silence instance=~^node-.* 10m` when testing a narrow label-based maintenance
silence. Use `/unsilence silence-id` or `/unsilence silence-id-1,silence-id-2`
only against silences created for the smoke test.
Other text and other chat IDs are ignored. If Alertmanager is unavailable, the
chat gets a short failure message while the service log keeps the detailed
error. Telegram transport errors redact the bot token before writing to logs.
