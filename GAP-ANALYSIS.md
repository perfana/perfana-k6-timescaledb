# Feature parity with perfana-jmeter-timescaledb

Verdict: **close to full parity when built against the Breaking-IT k6 fork; on
upstream k6 the error detail and real URLs are missing** (see
[Requirements](README.md#requirements) for what each build records).
Three columns remain unreachable without a patch to k6 itself, and they are
listed under [What is still missing](#what-is-still-missing). The schema, the
batching/backpressure behaviour and every run-identification option port over
exactly. What does not port is anything a k6 *output* cannot see, because a k6
output receives metric samples only — never a request or a response object.

Both plugins write the same five tables, so a Perfana instance can consume k6
and JMeter runs side by side without schema changes.

## Why the fork is required

A JMeter `BackendListenerClient` is handed the whole `SampleResult`: URL,
request headers, response headers, response body, assertion results and the
live thread variables. A k6 `output.Output` is handed `[]metrics.SampleContainer`
— numbers plus low-cardinality tags. On upstream k6, bodies and headers never
enter the metric stream, so no output extension can reach them.

The [Breaking-IT k6 fork](https://github.com/Breaking-IT/k6) closes that: with
`httpErrorData` enabled it attaches the request headers, request body, response
headers and response body to the `http_req_failed` sample of a failed request
(four fields on `metrics.Sample`). This output reads them straight off the
trail, so `requests_error` is complete without a single change to a test script.

## What is still missing

| Column | Why | Fixable |
|---|---|---|
| `requests_raw.request_size` / `response_size` | k6 emits `data_sent`/`data_received` per *iteration*, from the dialer, never per request | yes, ~20 lines in the fork — see the [addendum](#still-open-in-the-fork-closable-with-a-small-patch) |
| tying a failed check to its request | `checks` is its own metric, tagged with the check name, group and scenario and nothing else | partly: failed checks are recorded as their own error rows, attributed to the transaction; per-request attribution needs a request id in the fork |
| `requests_error.session_variables` | k6 has no session state to snapshot; the failed request's own body, which *is* captured, usually carries the correlating ids | n/a by design |
| `virtual_users.started_threads` / `finished_threads` | k6 has no cumulative VU counters | yes, but k6 reuses VUs so the numbers would not mean what JMeter's do |

## Column-by-column

### `requests_raw`

| Column | k6 source | Parity |
|---|---|---|
| `time` | trail end time | ✅ |
| `test_run_id`, `system_under_test`, `test_environment`, `scenario_name`, `location` | config | ✅ |
| `transaction_name` | `transaction` tag, else the `group` tag (outermost or innermost, see `flattenNestedTransactions`) | ✅ |
| `sampler_name` | `name` tag | ⚠️ k6's `name` is the raw URL unless the script uses [URL grouping](https://grafana.com/docs/k6/latest/using-k6/http-requests/#url-grouping) or a `name` tag |
| `success` | `expected_response` verdict, falling back to error tag / status | ✅ |
| `response_code` | `status` tag | ✅ |
| `response_time` | `http_req_connecting + http_req_tls_handshaking + http_req_duration` | ⚠️ see [Timings](#timings) |
| `response_latency` | `... + http_req_sending + http_req_waiting` | ⚠️ idem |
| `response_connect_time` | `http_req_connecting + http_req_tls_handshaking` | ✅ |
| `url_hash` | md5 of the normalized URL, from `request_url` metadata (`httpRequestUrl`) or the `url` tag | ✅ |
| `request_size` | — | ❌ **always NULL** |
| `response_size` | — | ❌ **always NULL** |
| `source_element_path` | scenario → groups → transaction tag → `controllers` tag frames → request | ⚠️ structure must be declared by the script, `occurrence` always 0 |

Sizes are left NULL: there is no per-request byte count anywhere in the metric
stream. The fork could add one cheaply — see the addendum.

### `transactions`

| Column | k6 source | Parity |
|---|---|---|
| `transaction_name` | `group` tag | ✅ |
| `response_time` | `group_duration` | ✅ |
| `success` | derived: counts failed requests inside the group | ⚠️ see below |
| `request_size`, `response_size` | — | ❌ always NULL |

k6 groups carry no pass/fail verdict of their own. The output counts requests
that failed inside each group and lets each `group_duration` sample consume one,
so the failed/total ratio per transaction is always right. Which specific row a
failure lands on is only exact when the script enables the `vu` and `iter`
system tags (`options.systemTags` or `--system-tags`), which k6 leaves off by
default; without them a concurrent VU's row can absorb the failure.

A request outside any group becomes its own single-step transaction, matching
the JMeter plugin's handling of a sampler with no Transaction Controller.

A `transaction` tag on the request names the transaction directly, the way the
listener's `Transaction::sampler` label convention does, and takes precedence
over the group. One thing is deliberately *not* ported with it: the listener's
`planUsesTransactionControllers` latch, which suppresses single-step transaction
rows once any Transaction Controller has been seen. Its own comment says it
exists because "SampleResult.getParent() ... breaks intermittently under load",
so an unattributed sampler may be a linkage failure rather than a standalone
one. k6 puts the group on the sample itself, so an ungrouped request really is
ungrouped and gets its transaction row.

### `requests_error`

One row per failed request, written automatically.

| Column | k6 source | Parity |
|---|---|---|
| `time`, names, `response_code`, `response_time`, `connection_time`, `url`, `url_hash`, `random_id` | trail and tags | ✅ |
| `response_message` | `error` tag, else `HTTP <status>` | ✅ |
| `request_headers` | `httpErrorData` request headers, with the body below them | ✅ |
| `response_headers` | `httpErrorData` response headers | ✅ |
| `response_data` | `httpErrorData` response body, gated by `saveResponseBody` | ✅ |
| `assertions` | the check text, on rows produced by a failed `check()` | ⚠️ its own row, never on the request's row |
| `session_variables` | — | ❌ always NULL |

Non-UTF-8 bodies are stored as `<binary>`, which is the fork's behaviour and
close to the JMeter listener's `"binary"` placeholder.

### `virtual_users`

| Column | k6 source | Parity |
|---|---|---|
| `active_threads` | `vus` gauge | ✅ |
| `started_threads` | `vus_max` (initialized VUs) | ⚠️ closest analogue, not a cumulative count |
| `finished_threads` | — | ❌ always NULL |

k6 has no cumulative started/finished VU counters; `JMeterContextService.ThreadCounts`
has no equivalent.

### `url_patterns`

Full parity. The normalizer is a direct port, so `url_hash` values are
byte-identical to the JMeter plugin's and the table can be shared.

## Configuration

| JMeter parameter | k6 option | Notes |
|---|---|---|
| `timescaleDBHost/Port/Database/User/Password/SslMode` | `url` | one postgres DSN |
| `timescaleDBSchema` | `schema` | ✅ |
| `timescaleDBMaxPoolSize` | `maxPoolSize` | ✅ |
| `timescaleDBConnectionTimeout` | — | in the DSN (`connect_timeout=`) |
| `timescaleDBBatchSize`, `timescaleDBFlushInterval` | `batchSize`, `flushInterval` | ✅ |
| `runId`, `systemUnderTest`, `testEnvironment`, `scenarioName`, `location`, `nodeName` | same names | ✅ |
| `syntheticMonitoring` | `syntheticMonitoring` | ✅ incl. run id = scenario name |
| `saveResponseBody` | `saveResponseBody` | ✅ |
| `normalizeUrls` | `normalizeUrls` | ✅ |
| `flattenNestedTransactions` | `flattenNestedTransactions` | ✅ groups instead of Transaction Controllers |
| `saveSessionVariables`, `sessionVariablesInclude`, `sessionVariablesMaxValueLength`, `sessionVariablesMaxTotalBytes` | — | k6 has no session state to capture |

## Timings

k6's `http_req_duration` excludes connection setup; JMeter's elapsed time
includes it. This plugin uses JMeter's definition, which is what keeps
`response_connect_time ≤ response_latency ≤ response_time` true for every row —
it is not true if `http_req_duration` is copied verbatim. To recover the raw k6
number: `response_time - response_connect_time`. On reused connections
(the overwhelming majority of requests in a load test) the two are identical.

## Not covered

* **Non-HTTP protocols.** gRPC, WebSocket and browser-module metrics do not
  produce an HTTP trail and are not written to any table. JMeter records every
  sampler type.
* **Distributed runs.** Each k6 instance writes independently with its own
  `nodeName`, same as multiple JMeter instances. Untested against k6 Cloud.

## Addendum: the Breaking-IT k6 fork

[`Breaking-IT/k6`](https://github.com/Breaking-IT/k6) (`~/workspace/k6`, branch
`breakingit`, base k6 v2.1.0) changes this analysis: most of what is impossible
against upstream k6 is already solved there, and the rest is a small patch.

### Not yet published: the request URL of a named request

k6 replaces the `url` tag with the `name` tag as soon as a script names its
requests, to keep the indexed tag low-cardinality — and the URL then reaches no
output at all. The fork's `httpRequestUrl` option carries it as sample metadata
(`request_url`), which is not indexed in atlas, so `url_patterns` and
`requests_error.url` hold real URLs again. Without it both record sampler names.
The option exists only on an unpushed local branch so far; the published
`breakingit` branch lacks it, and this output reads the metadata if present.

### Already solved: error bodies and headers (in use)

The fork adds an `httpErrorData` option (`K6_HTTP_ERROR_DATA`) and four fields
on `metrics.Sample`, populated on the `http_req_failed` sample of a failed
request only:

```go
// metrics/sample.go
HTTPErrorReqHeaders string
HTTPErrorReqBody    string
HTTPErrorResHeaders string
HTTPErrorResBody    string
```

That sample is part of the trail this output reads, so `request_headers`,
`response_headers` and `response_data` are filled automatically for every failed
request. This is what the output does today; there is no JS module and no test
script involvement.

### Independent confirmation of the timing mapping

The fork's own `timescaledb-breakingit` output computes exactly the mapping used
here — `connect = connecting + tls_handshaking`, `latency = connect + sending +
waiting`, `response_time = connect + http_req_duration` — and labels it
"matching JMeter definitions".

### Still open in the fork, closable with a small patch

The fork's own output punts on the same three columns this one does
(`request_size` is literally `"" // request_size unknown`, started/finished
threads are `""`, and `transactions.success` is hard-coded `"true"`).

| Gap | Fork change | Size |
|---|---|---|
| `request_size` / `response_size` | `MakeRequest` already holds `respReq.Headers`/`Body` and `res.Header`/`resp.Body`; sum their lengths into two new `Trail` fields | ~20 lines, no new metrics, no double counting with the per-iteration `data_sent`/`data_received` |
| `transactions.success` exact | add `TagVU\|TagIter` to `DefaultSystemTagSet`, making this output's per-(vu, iteration, group) attribution exact | 1 line; costs metadata on every sample |
| `transactions.success` exact, cheaper at runtime | track a per-group failure count in the VU state, emit it as a tag on `group_duration` | ~40 lines in `internal/js/modules/k6/k6.go` plus `lib.State` |
| `assertions` tied to their request | stamp a per-VU request sequence id as trail metadata and auto-tag `check()` when its argument is an HTTP response | ~50 lines, the only fiddly one |
| `virtual_users.started_threads` / `finished_threads` | cumulative counters in `lib.ExecutionState`, emitted next to `vus`/`vus_max` | ~30 lines, but k6 reuses VUs across iterations so the numbers do not mean what JMeter's thread counts mean |

Per-request wire bytes remain unavailable on HTTP/2, where connection-level
counters cannot be attributed to a stream. Header + body length — what the patch
above measures, and what JMeter reports — is the right number anyway.

### Build implication

On `main` the fork-only reads sit behind a `k6fork` build tag. On this
`breakingit-fork` branch they are the only build: `go.mod` replaces
`go.k6.io/k6/v2` with a pinned commit of the published fork.

### Overlap to resolve

The fork already ships a `timescaledb-breakingit` output writing a related but
different schema (no `system_under_test`/`test_environment`, UUIDv7 `id` instead
of `random_id`, `thread_group_name`, `_sm` table variants, written through a
pg-proxy HTTP endpoint rather than direct Postgres). It also covers WebSocket,
browser and custom trend metrics, which this output does not, and it maps failed
`checks` into `requests_error` rows — a trick that fills the `assertions` gap
with no fork change at all.
