# Perfana k6 TimescaleDB Output

A [k6](https://k6.io) output extension that writes test results directly to
[TimescaleDB](https://www.timescale.com/), into the same schema as
[perfana-jmeter-timescaledb](https://github.com/perfana/perfana-jmeter-timescaledb).
Perfana consumes k6 and JMeter runs through identical tables.

See [GAP-ANALYSIS.md](GAP-ANALYSIS.md) for exactly where k6 can and cannot match
the JMeter listener.

## Requirements

Builds against upstream k6 (`go.k6.io/k6/v2`) by default. Two columns then stay
empty, because upstream keeps the data out of the metric stream entirely:
`requests_error` has no headers or body, and a request that sets a `name` tag
records that name as its url — k6 overwrites the `url` tag with the `name` tag,
so `url_patterns` collects sampler names. The output says so once at startup.

Building with `-tags k6fork` against the
[Breaking-IT k6 fork](https://github.com/Breaking-IT/k6) fills both, via its
`httpErrorData` and `httpRequestUrl` options:

```bash
export K6_HTTP_ERROR_DATA=true
export K6_HTTP_REQUEST_URL=true
```

Set them as environment variables rather than in `export const options`, so the
same script also runs on upstream k6 — which warns about option fields it does
not know, but ignores unknown `K6_*` variables silently.

| | upstream k6 | fork, `-tags k6fork` |
|---|---|---|
| `requests_raw`, `transactions`, `virtual_users`, `source_element_path` | ✅ | ✅ |
| `requests_error` rows | ✅ | ✅ |
| `requests_error` headers and body | ❌ | ✅ |
| real URLs in `url_patterns` / `requests_error.url` | ❌ named requests record their name | ✅ |

## Features

- Batched `COPY` writes with a configurable flush interval
- Connection pooling (pgx)
- Backpressure handling: optional payload is dropped when the database falls behind
- URL normalization into the shared `url_patterns` table
- Standard load testing and synthetic monitoring modes
- Virtual user tracking
- Full error rows — headers and body — with no test script changes

## Build

With [xk6](https://github.com/grafana/xk6) (needs Go 1.25+):

```bash
go install go.k6.io/xk6/cmd/xk6@latest

# upstream k6
xk6 build v2.2.0 --k6-repo go.k6.io/k6/v2 \
  --with github.com/perfana/perfana-k6-timescaledb

# Breaking-IT fork, with the full error detail and real URLs
git clone https://github.com/Breaking-IT/k6 ../k6
xk6 build --k6-repo go.k6.io/k6/v2 --replace go.k6.io/k6/v2=../k6 \
  --build-flags=-trimpath --build-flags="-ldflags=-s -w" --build-flags=-tags=k6fork \
  --with github.com/perfana/perfana-k6-timescaledb=.
```

`go build` and `go test` target upstream; add `-tags k6fork` with the module
replaced to exercise the other half.

## Database setup

```bash
for f in migrations/*.sql; do psql -h localhost -d k6 -f "$f"; done
```

`requests_raw.source_element_path` is probed at startup: on a database that
predates that migration the plugin logs once and keeps writing every other
column.

## Usage

```bash
./k6 run script.js \
  --out 'perfana-timescaledb=url=postgres://k6:secret@localhost:5432/k6?sslmode=require,runId=run-42,systemUnderTest=shop,testEnvironment=acc,scenarioName=checkout'
```

Every option can also be set as an environment variable, which is usually easier
in CI. The argument wins over the environment:

```bash
export K6_TIMESCALEDB_URL='postgres://k6:secret@localhost:5432/k6?sslmode=require'
export K6_TIMESCALEDB_RUN_ID=run-42
export K6_TIMESCALEDB_SYSTEM_UNDER_TEST=shop
./k6 run script.js --out perfana-timescaledb
```

## Options

| Option | Env var | Default | Description |
|---|---|---|---|
| `url` | `K6_TIMESCALEDB_URL` | `postgres://localhost:5432/k6?sslmode=prefer` | Postgres DSN |
| `schema` | `K6_TIMESCALEDB_SCHEMA` | `public` | Schema holding the tables |
| `maxPoolSize` | `K6_TIMESCALEDB_MAX_POOL_SIZE` | `10` | Maximum connections |
| `batchSize` | `K6_TIMESCALEDB_BATCH_SIZE` | `1000` | Rows per batch |
| `flushInterval` | `K6_TIMESCALEDB_FLUSH_INTERVAL` | `1` | Seconds, or a Go duration like `500ms` |
| `runId` | `K6_TIMESCALEDB_RUN_ID` | `Run` | Test run identifier |
| `systemUnderTest` | `K6_TIMESCALEDB_SYSTEM_UNDER_TEST` | `SUT` | System under test |
| `testEnvironment` | `K6_TIMESCALEDB_TEST_ENVIRONMENT` | `test` | Test environment |
| `scenarioName` | `K6_TIMESCALEDB_SCENARIO_NAME` | `Scenario` | Scenario name |
| `location` | `K6_TIMESCALEDB_LOCATION` | `local` | Deployment location |
| `nodeName` | `K6_TIMESCALEDB_NODE_NAME` | `controller` | Node name |
| `syntheticMonitoring` | `K6_TIMESCALEDB_SYNTHETIC_MONITORING` | `false` | Key rows by scenario, skip virtual user tracking |
| `saveResponseBody` | `K6_TIMESCALEDB_SAVE_RESPONSE_BODY` | `true` | Store response bodies of failed requests |
| `normalizeUrls` | `K6_TIMESCALEDB_NORMALIZE_URLS` | `true` | Fill `url_hash` and `url_patterns` |
| `flattenNestedTransactions` | `K6_TIMESCALEDB_FLATTEN_NESTED_TRANSACTIONS` | `true` | Record only the outermost group |
| `saveFailedChecks` | `K6_TIMESCALEDB_SAVE_FAILED_CHECKS` | `true` | Record a failed `check()` as an error row |

> ⚠️ **PII / secrets.** Failed requests store their request headers, request
> body, response headers and response body in TimescaleDB and its backups. Turn
> `httpErrorData` off, or `saveResponseBody=false` for the response body alone,
> on tests that handle sensitive data.

## Groups are transactions

A k6 `group()` maps onto a JMeter Transaction Controller.

`flattenNestedTransactions=true` (default) records only the outermost group: one
`transactions` row per outer group, and `requests_raw.transaction_name` set to
the outermost group whatever the nesting depth. Set it to `false` to get a row
per group level, with `transaction_name` set to the innermost enclosing group.

A request outside any group becomes its own single-step transaction.

### Naming a transaction without a group

A `transaction` tag on the request overrides the group, which is the analogue of
the JMeter listener's `Transaction::sampler` label convention. Use it where a
group will not reach — a `http.batch()` call, or an async flow:

```js
http.get(url, { tags: { name: 'add to cart', transaction: 'T01_Add_To_Cart' } });
```

With no enclosing group, each such request becomes its own `transactions` row
under that name, exactly as the labelled JMeter sampler does. Inside a group the
tag still names `requests_raw.transaction_name`, while the `transactions` row
keeps coming from the group — again matching the JMeter listener.

Transaction success is derived by counting failures inside the group. It is
exact per row only when the `vu` and `iter` system tags are enabled:

```js
export const options = { systemTags: ['proto', 'status', 'method', 'url', 'name',
  'group', 'check', 'error', 'error_code', 'expected_response', 'scenario', 'vu', 'iter'] };
```

Without them the failed/total ratio per transaction is still correct — see
[GAP-ANALYSIS.md](GAP-ANALYSIS.md#transactions).

## Failed checks

k6 does not let a failed `check()` fail its request: `http_req_failed` stays 0
and `expected_response` stays true, so `requests_raw.success` and the
transaction verdict both stay true — for every output, this one included. A
suite whose pass/fail logic lives in `check()` rather than in status codes would
otherwise record no failures at all.

So a failed check becomes its own `requests_error` row, with the check text in
`assertions` and `response_message`, and the enclosing group as the transaction.
The row is thin by necessity: a checks sample carries the check name, its group
and the scenario, and nothing else — there is no url, status or method on it,
and nothing ties it to the request it examined.

Two consequences worth knowing. A request that fails *and* whose check fails
produces two rows: one with the full detail, one assertion-only. And a failed
check still does not flip `success` on the request or the transaction, so error
counts drawn from `requests_raw` and from `requests_error` will disagree for
such a suite. Set `saveFailedChecks=false` where checks are advisory.

## Plan structure

`requests_raw.source_element_path` records where a request sits in the plan, and
Perfana renders a transaction's requests as that slice of the plan — parallel
groups, loops, conditionals. k6 has no controller elements, so a script that
wants the tree declares it with a `controllers` tag: `kind:name` frames,
outermost first, separated by `/`.

```js
http.get(url, { tags: { name: 'wishlist item', controllers: 'loop:Loop_Wishlist_x3' } });
http.get(url, { tags: { name: 'extra', controllers: 'loop:Loop_Wishlist_x3/conditional:If_Even_Iteration' } });
```

| kind | renders as | JMeter equivalent |
|---|---|---|
| `parallel` | Parallel group | Parallel Controller |
| `loop` | Loop | Loop / ForEach / While Controller |
| `conditional` | Conditional | If Controller |
| `alternating` | Alternating | Interleave / Random / Switch Controller |
| `transaction` | Transaction | nested Transaction Controller |

An unrecognised kind still gets a band, just an unlabelled one. Without the tag
the requests record normally and render as a flat list. The scenario and the
enclosing groups are added automatically, so only the structure k6 cannot
express needs declaring.

## Naming requests

`sampler_name` comes from k6's `name` tag, which defaults to the full URL. Give
requests a stable name so dashboards group them:

```js
http.get(`${base}/products/${id}`, { tags: { name: 'product detail' } });
```

See [examples/example.js](examples/example.js) for a runnable script.

## Tables

| Table | Description |
|---|---|
| `requests_raw` | Individual HTTP requests |
| `transactions` | Group aggregates |
| `requests_error` | Failed requests, with headers and body |
| `virtual_users` | VU counts over time |
| `url_patterns` | Normalized URL patterns |

## Development

```bash
go test ./...
xk6 build --k6-repo go.k6.io/k6/v2 --replace go.k6.io/k6/v2=../k6 \
  --with github.com/perfana/perfana-k6-timescaledb=.
```

## License

[Apache License 2.0](LICENSE)
