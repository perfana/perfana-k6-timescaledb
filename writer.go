package timescaledb

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sirupsen/logrus"
)

// Rows are held per table and flushed with COPY. Column order here must match
// the order values are appended in the add* methods.
type table struct {
	name string
	cols []string
	rows [][]any
}

func (t *table) add(vals ...any) { t.rows = append(t.rows, vals) }

type requestRaw struct {
	Time              time.Time
	TransactionName   string
	SamplerName       string
	Success           bool
	RequestSize       *int32
	ResponseSize      *int32
	ResponseCode      string
	ConnectTime       int32
	Latency           int32
	ResponseTime      int32
	URLHash           *string
	SourceElementPath []byte
}

type transaction struct {
	Time            time.Time
	TransactionName string
	Success         bool
	RequestSize     *int32
	ResponseSize    *int32
	ResponseTime    int32
}

type requestError struct {
	Time            time.Time
	TransactionName string
	SamplerName     string
	ResponseCode    string
	ResponseTime    *int32
	ConnectionTime  *int32
	URL             string
	URLHash         *string
	Assertions      *string
	ResponseMessage string
	RequestHeaders  *string
	ResponseHeaders *string
	ResponseData    *string
	RandomID        int32
}

type virtualUsers struct {
	Time            time.Time
	ActiveThreads   int32
	StartedThreads  *int32
	FinishedThreads *int32
}

type urlPattern struct {
	Hash       string
	Normalized string
	Original   string
	FirstSeen  time.Time
}

// Beyond this many buffered rows the writer sheds optional payload (response
// bodies, headers) the way the JMeter listener does under backpressure.
const bufferHighWaterMark = 50000

// ponytail: url_patterns are deduplicated with a plain map cleared wholesale
// when it grows past the cap. A real LRU only matters for runs with >100k
// distinct patterns, which would drown the table anyway.
const urlPatternCacheSize = 100000

// Committing url_patterns in small groups keeps the row locks that concurrent
// k6 instances collide on short-lived (same rationale as the JMeter plugin).
const urlPatternCommitGroup = 50

type writer struct {
	cfg    Config
	pool   *pgxpool.Pool
	logger logrus.FieldLogger

	requestsRaw   *table
	transactions  *table
	requestsError *table
	virtualUsers  *table

	urlPatterns []urlPattern
	knownURLs   map[string]struct{}

	hasSourceElementPath bool

	underPressure atomic.Bool
	dropped       atomic.Int64
}

func newWriter(ctx context.Context, cfg Config, logger logrus.FieldLogger) (*writer, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	poolCfg.MaxConns = int32(cfg.MaxPoolSize) //nolint:gosec // operator-supplied pool size
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("cannot reach database: %w", err)
	}

	w := &writer{cfg: cfg, pool: pool, logger: logger, knownURLs: map[string]struct{}{}}

	// The optional column may be missing on a database that predates the
	// migration; write everything else rather than failing every insert.
	w.hasSourceElementPath = w.hasColumn(ctx, "requests_raw", "source_element_path")

	rawCols := []string{
		"time", "test_run_id", "system_under_test", "test_environment", "scenario_name", "location",
		"transaction_name", "sampler_name", "success", "request_size", "response_size", "response_code",
		"response_connect_time", "response_latency", "response_time", "url_hash",
	}
	if w.hasSourceElementPath {
		rawCols = append(rawCols, "source_element_path")
	} else {
		logger.Info("column requests_raw.source_element_path is absent; plan path capture disabled")
	}
	errCols := []string{
		"time", "test_run_id", "system_under_test", "test_environment", "scenario_name", "location",
		"node_name", "transaction_name", "sampler_name", "response_code", "response_time",
		"connection_time", "url", "url_hash", "assertions", "response_message", "request_headers",
		"response_headers", "response_data", "random_id",
	}

	w.requestsRaw = &table{name: "requests_raw", cols: rawCols}
	w.requestsError = &table{name: "requests_error", cols: errCols}
	w.transactions = &table{name: "transactions", cols: []string{
		"time", "test_run_id", "system_under_test", "test_environment", "scenario_name", "location",
		"transaction_name", "success", "request_size", "response_size", "response_time",
	}}
	w.virtualUsers = &table{name: "virtual_users", cols: []string{
		"time", "test_run_id", "system_under_test", "test_environment", "scenario_name", "location",
		"node_name", "active_threads", "started_threads", "finished_threads",
	}}
	return w, nil
}

func (w *writer) hasColumn(ctx context.Context, tableName, column string) bool {
	var n int
	err := w.pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
		w.cfg.Schema, tableName, column).Scan(&n)
	if err != nil {
		w.logger.WithError(err).Warnf("could not probe %s.%s, assuming absent", tableName, column)
		return false
	}
	return n > 0
}

// common returns the six run-identifying columns every table starts with.
func (w *writer) common(t time.Time) []any {
	return []any{
		t, w.cfg.EffectiveRunID(), w.cfg.SystemUnderTest, w.cfg.TestEnvironment,
		w.cfg.ScenarioName, w.cfg.Location,
	}
}

func (w *writer) addRequestRaw(r requestRaw) {
	vals := append(w.common(r.Time),
		r.TransactionName, r.SamplerName, r.Success, r.RequestSize, r.ResponseSize,
		r.ResponseCode, r.ConnectTime, r.Latency, r.ResponseTime, r.URLHash)
	if w.hasSourceElementPath {
		vals = append(vals, nullableJSON(r.SourceElementPath))
	}
	w.requestsRaw.add(vals...)
}

func (w *writer) addTransaction(t transaction) {
	w.transactions.add(append(w.common(t.Time),
		t.TransactionName, t.Success, t.RequestSize, t.ResponseSize, t.ResponseTime)...)
}

func (w *writer) addRequestError(e requestError) {
	// Assertions is set only on rows that came from a failed check: nothing ties
	// a check back to the request it examined, so the two never share a row.
	w.requestsError.add(append(w.common(e.Time),
		w.cfg.NodeName, e.TransactionName, e.SamplerName, e.ResponseCode, e.ResponseTime,
		e.ConnectionTime, e.URL, e.URLHash, e.Assertions, e.ResponseMessage,
		e.RequestHeaders, e.ResponseHeaders, e.ResponseData, e.RandomID)...)
}

func (w *writer) addVirtualUsers(v virtualUsers) {
	w.virtualUsers.add(append(w.common(v.Time),
		w.cfg.NodeName, v.ActiveThreads, v.StartedThreads, v.FinishedThreads)...)
}

func (w *writer) addURLPattern(p urlPattern) {
	if _, seen := w.knownURLs[p.Hash]; seen {
		return
	}
	if len(w.knownURLs) >= urlPatternCacheSize {
		w.knownURLs = map[string]struct{}{}
	}
	w.knownURLs[p.Hash] = struct{}{}
	w.urlPatterns = append(w.urlPatterns, p)
}

func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func (w *writer) isUnderPressure() bool { return w.underPressure.Load() }

func (w *writer) pending() int {
	return len(w.requestsRaw.rows) + len(w.transactions.rows) +
		len(w.requestsError.rows) + len(w.virtualUsers.rows)
}

// flush writes every buffered row. It runs on the flusher goroutine and blocks,
// which is the backpressure: a slow database makes flushes overrun the interval,
// and that is what flips underPressure so optional payload gets dropped.
func (w *writer) flush(ctx context.Context) {
	if w.pending() == 0 && len(w.urlPatterns) == 0 {
		w.underPressure.Store(false)
		return
	}
	start := time.Now()
	rows := w.pending()

	for _, t := range []*table{w.requestsRaw, w.transactions, w.requestsError, w.virtualUsers} {
		if len(t.rows) == 0 {
			continue
		}
		_, err := w.pool.CopyFrom(ctx, pgx.Identifier{w.cfg.Schema, t.name}, t.cols, pgx.CopyFromRows(t.rows))
		if err != nil {
			w.dropped.Add(int64(len(t.rows)))
			w.logger.WithError(err).Errorf("failed writing %d rows to %s", len(t.rows), t.name)
		}
		t.rows = t.rows[:0]
	}
	w.flushURLPatterns(ctx)

	took := time.Since(start)
	w.underPressure.Store(took > w.cfg.FlushInterval || rows > bufferHighWaterMark)
	if w.isUnderPressure() {
		w.logger.Warnf("timescaledb writer is behind: %d rows took %s (flush interval %s); "+
			"dropping response bodies and headers until it catches up", rows, took, w.cfg.FlushInterval)
	}
}

func (w *writer) flushURLPatterns(ctx context.Context) {
	sql := fmt.Sprintf(
		`INSERT INTO %s.url_patterns
		   (url_hash, system_under_test, test_environment, normalized_url, original_example, first_seen)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (url_hash, system_under_test, test_environment) DO NOTHING`,
		pgx.Identifier{w.cfg.Schema}.Sanitize())

	for start := 0; start < len(w.urlPatterns); start += urlPatternCommitGroup {
		end := min(start+urlPatternCommitGroup, len(w.urlPatterns))
		batch := &pgx.Batch{}
		for _, p := range w.urlPatterns[start:end] {
			batch.Queue(sql, p.Hash, w.cfg.SystemUnderTest, w.cfg.TestEnvironment,
				p.Normalized, p.Original, p.FirstSeen)
		}
		if err := w.pool.SendBatch(ctx, batch).Close(); err != nil {
			w.logger.WithError(err).Error("failed upserting url patterns")
		}
	}
	w.urlPatterns = w.urlPatterns[:0]
}

func (w *writer) close(ctx context.Context) {
	w.flush(ctx)
	if n := w.dropped.Load(); n > 0 {
		w.logger.Errorf("timescaledb output dropped %d rows due to write errors", n)
	}
	w.pool.Close()
}
