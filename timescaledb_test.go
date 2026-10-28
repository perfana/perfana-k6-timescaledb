package timescaledb

import (
	"encoding/json"
	"testing"
	"time"

	"go.k6.io/k6/v2/lib/netext/httpext"
	"go.k6.io/k6/v2/metrics"
	"gopkg.in/guregu/null.v3"
)

func TestNormalizeURL(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"/api/users/12345/orders", "/api/users/{id}/orders"},
		{"/api/v1/f47ac10b-58cc-4372-a567-0e02b2c3d479", "/api/v1/{uuid}"},
		{"/download/abcdefghijklmnopqrstuvwxyz", "/download/{token}"},
		{"/search?z=1&a=hello", "/search?a={val}&z={val}"},
		{"/health", "/health"},
		{"https://h.example.nl/wbs/home;jsessionid=2o84pngkkda0o0hf!-230347638", "https://h.example.nl/wbs/home;jsessionid={val}"},
		{"https://host/api/assignments/1-q02oj1h/report/job-application", "https://host/api/assignments/{id}/report/job-application"},
		{"https://host/api/v2/oauth2/base64/sha256/html5/reports", "https://host/api/v2/oauth2/base64/sha256/html5/reports"},
		{"https://web3app0110.farm.example.com/api/assignments/1-ov4i2iw", "https://web3app0110.farm.example.com/api/assignments/{id}"},
		{"https://host/api/vacature-relation/58741300/save", "https://host/api/vacature-relation/{id}/save"},
	}
	for _, c := range cases {
		if got := normalizeURL(c.in).normalized; got != c.want {
			t.Errorf("normalizeURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// The hash is the join key of the shared url_patterns table: it must be the
	// MD5 of the normalized form, not of the original URL.
	if got := normalizeURL("/api/users/1").hash; got != md5Hex("/api/users/{id}") {
		t.Errorf("hash is not the md5 of the normalized url: %s", got)
	}
}

func TestParseConfig(t *testing.T) {
	t.Parallel()
	cfg, err := ParseConfig("runId=r1,flushInterval=5,normalizeUrls=false",
		map[string]string{"K6_TIMESCALEDB_SYSTEM_UNDER_TEST": "shop", "K6_TIMESCALEDB_RUN_ID": "from-env"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RunID != "r1" {
		t.Errorf("output argument must win over the environment, got %q", cfg.RunID)
	}
	if cfg.SystemUnderTest != "shop" {
		t.Errorf("SystemUnderTest = %q", cfg.SystemUnderTest)
	}
	if cfg.FlushInterval != 5*time.Second {
		t.Errorf("bare flushInterval must mean seconds, got %s", cfg.FlushInterval)
	}
	if cfg.NormalizeURLs {
		t.Error("normalizeUrls=false was ignored")
	}
	if _, err := ParseConfig("nope=1", nil); err == nil {
		t.Error("an unknown option should be rejected, not silently ignored")
	}
	if _, err := ParseConfig("errorSource=carrier-pigeon", nil); err == nil {
		t.Error("an invalid errorSource should be rejected")
	}

	synthetic, err := ParseConfig("syntheticMonitoring=true,scenarioName=checkout,runId=r1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if synthetic.EffectiveRunID() != "checkout" {
		t.Errorf("synthetic monitoring keys rows by scenario, got %q", synthetic.EffectiveRunID())
	}
}

func TestGroupSegments(t *testing.T) {
	t.Parallel()
	if got := groupSegments(""); got != nil {
		t.Errorf("no group should give no segments, got %v", got)
	}
	if got := groupSegments("::checkout::pay"); len(got) != 2 || got[0] != "checkout" || got[1] != "pay" {
		t.Errorf("groupSegments = %v", got)
	}
}

func TestTrailFailed(t *testing.T) {
	t.Parallel()
	explicit := &httpext.Trail{Failed: null.BoolFrom(false)}
	if trailFailed(explicit, "500", "") {
		t.Error("k6's own expected_response verdict must win over the status code")
	}
	if !trailFailed(&httpext.Trail{}, "200", "dial tcp: timeout") {
		t.Error("a transport error is a failure whatever the status says")
	}
	if !trailFailed(&httpext.Trail{}, "503", "") {
		t.Error("5xx without a verdict is a failure")
	}
	if trailFailed(&httpext.Trail{}, "204", "") {
		t.Error("2xx without a verdict is a success")
	}
}

func TestSourceElementPath(t *testing.T) {
	t.Parallel()
	var elements []pathElement
	if err := json.Unmarshal(sourceElementPath("default", "", "", []string{"checkout", "pay"}, "POST /pay"), &elements); err != nil {
		t.Fatal(err)
	}
	want := []pathElement{
		{Name: "default", Class: scenarioClass},
		{Name: "checkout", Class: groupClass},
		{Name: "pay", Class: groupClass},
		{Name: "POST /pay", Class: requestClass},
	}
	if len(elements) != len(want) {
		t.Fatalf("got %d elements, want %d: %v", len(elements), len(want), elements)
	}
	for i := range want {
		if elements[i] != want[i] {
			t.Errorf("element %d = %v, want %v", i, elements[i], want[i])
		}
	}
}

func TestRedactedURL(t *testing.T) {
	t.Parallel()
	got := redactedURL("postgres://user:hunter2@db:5432/k6")
	if got != "postgres://***@db:5432/k6" {
		t.Errorf("credentials leaked into the description: %q", got)
	}
}

func TestSourceElementPathWithTransactionTag(t *testing.T) {
	t.Parallel()
	var elements []pathElement
	if err := json.Unmarshal(sourceElementPath("default", "T01_Checkout", "", nil, "add to cart"), &elements); err != nil {
		t.Fatal(err)
	}
	want := []pathElement{
		{Name: "default", Class: scenarioClass},
		{Name: "T01_Checkout", Class: groupClass},
		{Name: "add to cart", Class: requestClass},
	}
	if len(elements) != len(want) {
		t.Fatalf("got %v, want %v", elements, want)
	}
	for i := range want {
		if elements[i] != want[i] {
			t.Errorf("element %d = %v, want %v", i, elements[i], want[i])
		}
	}
}

// The consumer classifies a band by the last dot-segment of its class, drops
// *ThreadGroup entries and the entry named after the transaction, and drops the
// trailing request entry. Get any of that wrong and the tree renders flat.
func TestSourceElementPathControllers(t *testing.T) {
	t.Parallel()
	var elements []pathElement
	raw := sourceElementPath("webshop", "", "loop:Loop_Wishlist_x3/conditional:If_Even_Iteration",
		[]string{"T08_Controller_Showcase"}, "showcase_even_iteration_extra")
	if err := json.Unmarshal(raw, &elements); err != nil {
		t.Fatal(err)
	}
	want := []pathElement{
		{Name: "webshop", Class: "k6.ThreadGroup"},
		{Name: "T08_Controller_Showcase", Class: "k6.TransactionController"},
		{Name: "Loop_Wishlist_x3", Class: "k6.LoopController"},
		{Name: "If_Even_Iteration", Class: "k6.IfController"},
		{Name: "showcase_even_iteration_extra", Class: "k6.HTTPRequest"},
	}
	if len(elements) != len(want) {
		t.Fatalf("got %v, want %v", elements, want)
	}
	for i := range want {
		if elements[i] != want[i] {
			t.Errorf("element %d = %v, want %v", i, elements[i], want[i])
		}
	}
	if got := controllerElements("mystery:Some_Controller"); len(got) != 1 || got[0].Class != "k6.Controller" {
		t.Errorf("an unknown kind should still get a neutral band, got %v", got)
	}
	if got := controllerElements(""); got != nil {
		t.Errorf("no controllers should give no elements, got %v", got)
	}
}

// A failed check is the only failure signal a suite gets when its pass/fail
// logic lives in check() rather than in status codes, so it has to reach
// requests_error even though nothing ties it to a request.
func TestFailedCheckBecomesErrorRow(t *testing.T) {
	t.Parallel()

	registry := metrics.NewRegistry()
	checks := registry.MustNewMetric(metrics.ChecksName, metrics.Rate)
	sample := func(tags *metrics.TagSet, value float64) metrics.Sample {
		return metrics.Sample{
			TimeSeries: metrics.TimeSeries{Metric: checks, Tags: tags},
			Time:       time.Unix(1, 0),
			Value:      value,
		}
	}

	newOutput := func(cfg Config) *Output {
		return &Output{
			cfg:           cfg,
			groupFailures: map[string]int{},
			w: &writer{
				cfg:           cfg,
				requestsError: &table{name: "requests_error"},
			},
		}
	}

	cfg := defaultConfig()
	cfg.FlattenNestedTransactions = true
	tags := registry.RootTagSet().
		With("check", "body has no foutmelding").
		With("group", "::ESP_MetaDataUUID::inner")

	o := newOutput(cfg)
	o.addOtherSamples([]metrics.Sample{sample(tags, 0)})
	rows := o.w.requestsError.rows
	if len(rows) != 1 {
		t.Fatalf("expected one error row, got %d", len(rows))
	}
	// common() is time + the five run-identifying columns, then node_name,
	// transaction_name, sampler_name, response_code ...
	if got := rows[0][7]; got != "ESP_MetaDataUUID" {
		t.Errorf("transaction_name = %v, want the outermost group", got)
	}
	if got := rows[0][8]; got != "body has no foutmelding" {
		t.Errorf("sampler_name = %v, want the check text", got)
	}
	assertions, ok := rows[0][14].(*string)
	if !ok || *assertions != "body has no foutmelding" {
		t.Errorf("assertions = %v, want the check text", rows[0][14])
	}

	// A passing check is not an error.
	o = newOutput(cfg)
	o.addOtherSamples([]metrics.Sample{sample(tags, 1)})
	if len(o.w.requestsError.rows) != 0 {
		t.Errorf("a passing check must not produce an error row")
	}

	// Suites that treat checks as advisory can switch it off.
	cfg.SaveFailedChecks = false
	o = newOutput(cfg)
	o.addOtherSamples([]metrics.Sample{sample(tags, 0)})
	if len(o.w.requestsError.rows) != 0 {
		t.Errorf("saveFailedChecks=false must not produce an error row")
	}
}

// A failed check has to fail its transaction, the way a failed JMeter assertion
// fails the Transaction Controller around it. The request stays successful:
// nothing ties the check back to it.
func TestFailedCheckFailsItsTransaction(t *testing.T) {
	t.Parallel()

	registry := metrics.NewRegistry()
	checks := registry.MustNewMetric(metrics.ChecksName, metrics.Rate)
	groupDuration := registry.MustNewMetric(metrics.GroupDurationName, metrics.Trend)
	tags := registry.RootTagSet().
		With("check", "customer has sufficient funds").
		With("group", "::T01_Funds_Check")
	meta := map[string]string{"vu": "1", "iter": "7"}

	cfg := defaultConfig()
	o := &Output{
		cfg:           cfg,
		groupFailures: map[string]int{},
		w: &writer{
			cfg:           cfg,
			requestsError: &table{name: "requests_error"},
			transactions:  &table{name: "transactions"},
		},
	}

	// The check fails, then the group closes - the order k6 emits them in.
	o.addOtherSamples([]metrics.Sample{
		{TimeSeries: metrics.TimeSeries{Metric: checks, Tags: tags}, Metadata: meta, Value: 0},
		{TimeSeries: metrics.TimeSeries{Metric: groupDuration, Tags: tags}, Metadata: meta, Value: 12},
	})

	rows := o.w.transactions.rows
	if len(rows) != 1 {
		t.Fatalf("expected one transaction row, got %d", len(rows))
	}
	// common() is time + five run columns, then transaction_name, success.
	if got := rows[0][6]; got != "T01_Funds_Check" {
		t.Errorf("transaction_name = %v", got)
	}
	if success, _ := rows[0][7].(bool); success {
		t.Error("a transaction whose check failed must not be reported as passed")
	}
	if len(o.groupFailures) != 0 {
		t.Errorf("the group_duration sample should consume the mark, left %v", o.groupFailures)
	}

	// A failing request AND a failing check in one pass is still one verdict:
	// the group_duration consumes exactly one mark, so nothing may be left over
	// to fail an unrelated later transaction.
	o.markGroupFailure("1", "8", []string{"T01_Funds_Check"})
	o.markGroupFailure("1", "8", []string{"T01_Funds_Check"})
	o.addOtherSamples([]metrics.Sample{
		{TimeSeries: metrics.TimeSeries{Metric: groupDuration, Tags: tags}, Metadata: map[string]string{"vu": "1", "iter": "8"}, Value: 9},
	})
	if len(o.groupFailures) != 0 {
		t.Errorf("two failures in one pass must leave no residue, got %v", o.groupFailures)
	}
}

// The fork reports a browser request as a bundle of timings followed by a
// separate browser_http_req_failed bundle, or as one bundle when the request
// failed before any response. Each must become exactly one requests_raw row.
func TestBrowserRequests(t *testing.T) {
	t.Parallel()

	registry := metrics.NewRegistry()
	m := func(name string, typ metrics.MetricType) *metrics.Metric { return registry.MustNewMetric(name, typ) }
	duration := m(browserReqDuration, metrics.Trend)
	connecting := m(browserReqConnecting, metrics.Trend)
	tls := m(browserReqTLSHandshaking, metrics.Trend)
	waiting := m(browserReqWaiting, metrics.Trend)
	received := m(browserDataReceived, metrics.Counter)
	failed := m(browserReqFailed, metrics.Rate)

	newOutput := func() *Output {
		cfg := defaultConfig()
		return &Output{
			cfg:            cfg,
			groupFailures:  map[string]int{},
			pendingBrowser: map[browserKey]*browserRequest{},
			w: &writer{
				cfg:           cfg,
				requestsRaw:   &table{name: "requests_raw"},
				transactions:  &table{name: "transactions"},
				requestsError: &table{name: "requests_error"},
				knownURLs:     map[string]struct{}{},
			},
		}
	}
	url := "https://shop.example/products/12345/image.png"
	at := time.Unix(10, 0)
	timings := func(tags *metrics.TagSet) metrics.Samples {
		s := func(metric *metrics.Metric, v float64) metrics.Sample {
			return metrics.Sample{TimeSeries: metrics.TimeSeries{Metric: metric, Tags: tags}, Time: at, Value: v}
		}
		return metrics.Samples{s(duration, 20), s(connecting, 5), s(tls, 3), s(waiting, 12), s(received, 4096)}
	}
	verdict := func(tags *metrics.TagSet, v float64) metrics.Sample {
		return metrics.Sample{
			TimeSeries: metrics.TimeSeries{Metric: failed, Tags: tags}, Time: at, Value: v,
			Metadata: map[string]string{
				httpext.MetadataErrorReqHeaders: "GET /img HTTP/1.1", httpext.MetadataErrorResHeaders: "HTTP/2 404 Not Found",
			},
		}
	}
	tags := registry.RootTagSet().With("url", url).With("name", url)

	// Success: timings, then the verdict in its own bundle.
	o := newOutput()
	o.addBrowserRequest(timings(tags.With("status", "200")))
	if len(o.w.requestsRaw.rows) != 0 {
		t.Fatal("the timings must wait for their verdict")
	}
	o.addBrowserRequest(metrics.Samples{verdict(tags.With("status", "200"), 0)})
	rows := o.w.requestsRaw.rows
	if len(rows) != 1 {
		t.Fatalf("expected one requests_raw row, got %d", len(rows))
	}
	if got := rows[0][7]; got != "https://shop.example/products/{id}/image.png" {
		t.Errorf("sampler_name = %v, want the url pattern", got)
	}
	if size, _ := rows[0][10].(*int32); size == nil || *size != 4096 {
		t.Errorf("response_size = %v, want 4096", rows[0][10])
	}
	// JMeter timings: connect = connecting + tls, response = connect + duration.
	if rows[0][12] != int32(8) || rows[0][13] != int32(20) || rows[0][14] != int32(28) {
		t.Errorf("connect/latency/response = %v/%v/%v, want 8/20/28", rows[0][12], rows[0][13], rows[0][14])
	}
	if len(o.w.transactions.rows) != 0 || len(o.w.requestsError.rows) != 0 || len(o.pendingBrowser) != 0 {
		t.Error("a successful page asset must leave no transaction, error or pending entry")
	}

	// Failure with a response: the verdict carries the error detail.
	o = newOutput()
	o.addBrowserRequest(timings(tags.With("status", "404")))
	o.addBrowserRequest(metrics.Samples{verdict(tags.With("status", "404"), 1)})
	if len(o.w.requestsError.rows) != 1 {
		t.Fatalf("expected one error row, got %d", len(o.w.requestsError.rows))
	}
	if h, _ := o.w.requestsError.rows[0][17].(*string); h == nil || *h != "HTTP/2 404 Not Found" {
		t.Errorf("response_headers = %v", o.w.requestsError.rows[0][17])
	}

	// Failure before any response: one bundle holding timings and verdict.
	o = newOutput()
	refused := tags.With("status", "0").With("error", "net::ERR_CONNECTION_REFUSED")
	o.addBrowserRequest(append(timings(refused), verdict(refused, 1)))
	if len(o.w.requestsRaw.rows) != 1 || len(o.w.requestsError.rows) != 1 {
		t.Fatalf("got %d raw / %d error rows, want 1/1", len(o.w.requestsRaw.rows), len(o.w.requestsError.rows))
	}
	if got := o.w.requestsError.rows[0][15]; got != "net::ERR_CONNECTION_REFUSED" {
		t.Errorf("response_message = %v", got)
	}

	// No verdict ever (Chromium reported no response): written one flush later.
	o = newOutput()
	o.addBrowserRequest(timings(tags))
	o.flushPendingBrowser(false)
	if len(o.w.requestsRaw.rows) != 0 {
		t.Fatal("a verdict may still arrive in the next flush")
	}
	o.flushPendingBrowser(false)
	if len(o.w.requestsRaw.rows) != 1 || len(o.pendingBrowser) != 0 {
		t.Errorf("an orphaned request must be written after one flush")
	}

	// Stock k6: duration and bytes only, so connect and latency stay 0.
	o = newOutput()
	o.addBrowserRequest(metrics.Samples{timings(tags)[0], timings(tags)[4]})
	o.addBrowserRequest(metrics.Samples{verdict(tags, 0)})
	if rows := o.w.requestsRaw.rows; len(rows) != 1 || rows[0][12] != int32(0) || rows[0][14] != int32(20) {
		t.Errorf("a duration-only request must be written with response_time 20, got %v", rows)
	}

	// Served from cache (stock k6 reports these): skipped, verdict included.
	o = newOutput()
	cached := tags.With("from_cache", "true")
	if !o.addBrowserRequest(timings(cached)) || !o.addBrowserRequest(metrics.Samples{verdict(cached, 0)}) {
		t.Error("cached requests must be consumed")
	}
	o.flushPendingBrowser(true)
	if len(o.w.requestsRaw.rows) != 0 {
		t.Error("cached requests must not be written")
	}

	// Anything else is not ours.
	if newOutput().addBrowserRequest(metrics.Samples{{TimeSeries: metrics.TimeSeries{Metric: m("vus", metrics.Gauge), Tags: tags}}}) {
		t.Error("non-browser samples must fall through to addOtherSamples")
	}
}
