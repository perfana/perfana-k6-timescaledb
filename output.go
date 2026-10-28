package timescaledb

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"go.k6.io/k6/v2/lib"
	"go.k6.io/k6/v2/lib/netext/httpext"
	"go.k6.io/k6/v2/metrics"
	"go.k6.io/k6/v2/output"
)

const (
	outputName = "perfana-timescaledb"
	version    = "v0.1.0"

	// transactionTag names the transaction a request belongs to, overriding the
	// group. The Breaking-IT timescaledb output reads the same tag.
	transactionTag = "transaction"

	// controllersTag declares the plan structure enclosing a request, which k6
	// has no elements for: "kind:name" frames, outermost first, separated by "/"
	// (e.g. "loop:Loop_Wishlist_x3/conditional:If_Even_Iteration"). It is what
	// lets a k6 run render the same banded tree as a JMeter run.
	controllersTag = "controllers"

	// metadataRequestURL mirrors httpext.MetadataRequestURL on the perfana
	// fork. Upstream k6 never sets it, so the lookup simply misses.
	metadataRequestURL = "request_url"
)

// Classes in source_element_path are named after the JMeter elements Perfana
// already classifies: it keys off the LAST dot-segment of the class, drops
// entries whose class ends in ThreadGroup, and drops the one named after the
// transaction. Emitting anything else flattens the tree into one nameless band.
const (
	scenarioClass   = "k6.ThreadGroup"           // one per run, dropped by the consumer
	groupClass      = "k6.TransactionController" // outermost is the transaction itself
	requestClass    = "k6.HTTPRequest"           // trailing entry, dropped by the consumer
	controllerClass = "k6.Controller"            // unrecognised kind: still gets a band
)

// controllerKinds maps a frame kind onto the JMeter controller whose rendering
// it should borrow.
//
//nolint:gochecknoglobals // the kind->class table is the tag's contract
var controllerKinds = map[string]string{
	"parallel":    "k6.ParallelController",
	"loop":        "k6.LoopController",
	"conditional": "k6.IfController",
	"alternating": "k6.InterleaveController",
	"transaction": "k6.TransactionController",
}

func init() {
	output.RegisterExtension(outputName, New)
}

// Output writes k6 metrics into the Perfana TimescaleDB schema.
type Output struct {
	cfg    Config
	logger logrus.FieldLogger
	buf    output.SampleBuffer

	flusher *output.PeriodicFlusher
	w       *writer
	ctx     context.Context
	cancel  context.CancelFunc

	// groupFailures counts requests that failed inside a group, so a transaction
	// row can carry a success flag k6 does not emit itself. Each group_duration
	// sample consumes one. The key includes the vu and iteration when the script
	// enables those system tags, which makes the attribution exact; without them
	// it degrades to per-group counting, which keeps the failed/total ratio right
	// but may pin a failure on a concurrent VU's transaction row.
	groupFailures map[string]int

	// pendingBrowser holds browser requests waiting for their failed sample.
	pendingBrowser map[browserKey]*browserRequest
}

// New builds the output. k6 calls this before the test starts.
func New(params output.Params) (output.Output, error) {
	cfg, err := ParseConfig(params.ConfigArgument, params.Environment)
	if err != nil {
		return nil, err
	}
	logger := params.Logger.WithField("output", outputName)
	warnMissingCapabilities(logger, params.ScriptOptions)
	return &Output{
		cfg:            cfg,
		logger:         logger,
		groupFailures:  map[string]int{},
		pendingBrowser: map[browserKey]*browserRequest{},
	}, nil
}

func (o *Output) Description() string {
	return fmt.Sprintf("%s %s [%s] (%s, run %s)", outputName, version, buildTarget,
		redactedURL(o.cfg.URL), o.cfg.EffectiveRunID())
}

func (o *Output) Start() error {
	o.ctx, o.cancel = context.WithCancel(context.Background())
	w, err := newWriter(o.ctx, o.cfg, o.logger)
	if err != nil {
		o.cancel()
		return err
	}
	o.w = w

	flusher, err := output.NewPeriodicFlusher(o.cfg.FlushInterval, o.flush)
	if err != nil {
		o.w.close(o.ctx)
		o.cancel()
		return err
	}
	o.flusher = flusher
	return nil
}

func (o *Output) Stop() error {
	o.flusher.Stop()
	o.flushPendingBrowser(true)
	o.w.close(o.ctx)
	o.cancel()
	return nil
}

func (o *Output) AddMetricSamples(samples []metrics.SampleContainer) {
	o.buf.AddMetricSamples(samples)
}

// flush maps one interval of buffered samples onto rows and writes them. It is
// the only goroutine touching the writer buffers, so they need no locking.
func (o *Output) flush() {
	for _, container := range o.buf.GetBufferedSamples() {
		if trail, ok := container.(*httpext.Trail); ok {
			o.addRequest(trail, false, nil)
			continue
		}
		if o.addBrowserRequest(container.GetSamples()) {
			continue
		}
		o.addOtherSamples(container.GetSamples())
	}
	o.flushPendingBrowser(false)
	o.w.flush(o.ctx)
}

// addOtherSamples handles the containers that are not HTTP requests: the
// group_duration sample that becomes a transaction row, and the vus/vus_max
// pair that becomes a virtual_users row.
func (o *Output) addOtherSamples(samples []metrics.Sample) {
	var vus, vusMax float64
	var vusTime time.Time
	haveVUs := false

	for _, s := range samples {
		switch s.Metric.Name {
		case metrics.GroupDurationName:
			o.addTransaction(s)
		case metrics.ChecksName:
			if s.Value == 0 {
				o.addFailedCheck(s)
			}
		case metrics.VUsName:
			vus, vusTime, haveVUs = s.Value, s.Time, true
		case metrics.VUsMaxName:
			vusMax = s.Value
		}
	}

	// Synthetic monitoring has no load profile to record, same as the JMeter plugin.
	if haveVUs && !o.cfg.SyntheticMonitoring {
		started := int32(vusMax) //nolint:gosec // VU counts are small
		o.w.addVirtualUsers(virtualUsers{
			Time:           vusTime,
			ActiveThreads:  int32(vus), //nolint:gosec // idem
			StartedThreads: &started,
		})
	}
}

// addRequest writes one request. A browser request is a page's sub-resource
// rather than a step of the script, so it never becomes a single-step
// transaction, and its sampler name is the URL pattern when the script did not
// name it (the fork sets the name tag to the full URL).
func (o *Output) addRequest(tr *httpext.Trail, browser bool, responseSize *int32) {
	name := tag(tr.Tags, "name")
	// A script that names its requests makes k6 overwrite the url tag with that
	// name, so the real URL survives only in metadata, and only on the fork with
	// httpRequestUrl enabled. Without it there is nothing to normalize and
	// url_patterns would record sampler names.
	url := tr.Metadata[metadataRequestURL]
	if url == "" {
		url = tag(tr.Tags, "url")
	}
	if url == "" {
		url = name
	}
	if browser && o.cfg.NormalizeURLs && name == url && url != "" {
		name = normalizeURL(url).normalized
	}
	status := tag(tr.Tags, "status")
	errorMsg := tag(tr.Tags, "error")
	segments := groupSegments(tag(tr.Tags, "group"))
	success := !trailFailed(tr, status, errorMsg)

	// Transaction attribution, in the JMeter listener's order of precedence: an
	// explicit transaction name on the request itself wins, then the enclosing
	// group, and a request with neither is its own single-step transaction.
	// The explicit tag is the analogue of the listener's "Transaction::sampler"
	// label convention: it names a transaction without needing a group, which
	// matters in k6 because a group cannot span a http.batch() or an async call.
	transactionName := name
	if explicit := tag(tr.Tags, transactionTag); explicit != "" {
		transactionName = explicit
	} else if len(segments) > 0 {
		if o.cfg.FlattenNestedTransactions {
			transactionName = segments[0]
		} else {
			transactionName = segments[len(segments)-1]
		}
	}

	var urlHash *string
	if o.cfg.NormalizeURLs && url != "" {
		n := normalizeURL(url)
		urlHash = &n.hash
		o.w.addURLPattern(urlPattern{Hash: n.hash, Normalized: n.normalized, Original: n.original, FirstSeen: tr.EndTime})
	}

	// JMeter's elapsed time runs from the start of the connection to the last
	// byte, so connect time is folded in; k6's http_req_duration leaves it out.
	// Keeping JMeter's definition is what makes response_connect_time <=
	// response_latency <= response_time hold in this schema.
	responseTime := ms(tr.ConnDuration + tr.Duration)
	o.w.addRequestRaw(requestRaw{
		Time:            tr.EndTime,
		TransactionName: transactionName,
		SamplerName:     name,
		Success:         success,
		ResponseSize:    responseSize,
		ResponseCode:    status,
		ConnectTime:     ms(tr.ConnDuration),
		// JMeter latency is time-to-first-byte, connection setup included.
		Latency:      ms(tr.ConnDuration + tr.Sending + tr.Waiting),
		ResponseTime: responseTime,
		URLHash:      urlHash,
		SourceElementPath: sourceElementPath(tag(tr.Tags, "scenario"), tag(tr.Tags, transactionTag),
			tag(tr.Tags, controllersTag), segments, name),
	})

	if len(segments) == 0 && !browser {
		o.w.addTransaction(transaction{
			Time:            tr.EndTime,
			TransactionName: transactionName,
			Success:         success,
			ResponseTime:    responseTime,
		})
	} else if !success {
		o.markGroupFailure(tr.Metadata["vu"], tr.Metadata["iter"], segments)
	}

	if !success {
		if errorMsg == "" {
			errorMsg = "HTTP " + status
		}
		connectTime := ms(tr.ConnDuration)
		row := requestError{
			Time:            tr.EndTime,
			TransactionName: transactionName,
			SamplerName:     name,
			ResponseCode:    status,
			ResponseTime:    &responseTime,
			ConnectionTime:  &connectTime,
			URL:             url,
			URLHash:         urlHash,
			ResponseMessage: errorMsg,
			RandomID:        rand.Int32N(1_000_000), //nolint:gosec // matches the JMeter plugin's random_id
		}
		// Optional payload is what gets shed first when the writer falls behind,
		// same as the JMeter listener.
		if !o.w.isUnderPressure() {
			detail := errorDetail(tr)
			row.RequestHeaders = nonEmpty(joinNonEmpty(detail.reqHeaders, detail.reqBody))
			row.ResponseHeaders = nonEmpty(detail.resHeaders)
			if o.cfg.SaveResponseBody {
				row.ResponseData = nonEmpty(detail.resBody)
			}
		}
		o.w.addRequestError(row)
	}
}

// httpErrorDetail is the request and response detail of a failed request. Only
// the perfana k6 fork puts it in the metric stream; see fork.go.
type httpErrorDetail struct{ reqHeaders, reqBody, resHeaders, resBody string }

// joinNonEmpty puts the request body below its headers, the way the JMeter
// listener lays out the request block.
func joinNonEmpty(headers, body string) string {
	if body == "" {
		return headers
	}
	return headers + "\n" + body
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// addFailedCheck records a failed check as an error row. A check is the closest
// k6 has to a JMeter assertion, and for a suite whose pass/fail logic lives in
// check() rather than in status codes it is the only failure signal there is -
// k6 does not let a failed check fail the request, so requests_raw.success and
// the transaction verdict both stay true.
//
// The row is thin by necessity: a checks sample is tagged with the check name,
// its group and the scenario, and nothing else. There is no url, status or
// method to record, and no way to tie it to the request it examined.
func (o *Output) addFailedCheck(s metrics.Sample) {
	if !o.cfg.SaveFailedChecks {
		return
	}
	check := tag(s.Tags, "check")
	if check == "" {
		check = "check failed"
	}

	segments := groupSegments(tag(s.Tags, "group"))
	transactionName := check
	if explicit := tag(s.Tags, transactionTag); explicit != "" {
		transactionName = explicit
	} else if len(segments) > 0 {
		if o.cfg.FlattenNestedTransactions {
			transactionName = segments[0]
		} else {
			transactionName = segments[len(segments)-1]
		}
	}

	// A script can tag a check with a name; without one the check text is all
	// there is to call the row.
	samplerName := tag(s.Tags, "name")
	if samplerName == "" {
		samplerName = check
	}

	// A script can tag a check with the request it examined - k6 carries those
	// tags onto the sample. Without them these stay empty, which is honest: a
	// check by itself knows nothing about a url or a status.
	checkURL := tag(s.Tags, "url")
	var urlHash *string
	if o.cfg.NormalizeURLs && checkURL != "" {
		// Hash only. The request's own row already registered the pattern, and
		// its original_example is a real URL rather than whatever the check
		// happened to be tagged with.
		n := normalizeURL(checkURL)
		urlHash = &n.hash
	}

	o.w.addRequestError(requestError{
		Time:            s.Time,
		TransactionName: transactionName,
		SamplerName:     samplerName,
		URL:             checkURL,
		URLHash:         urlHash,
		// Both columns carry the check text: assertions is where a JMeter-shaped
		// consumer looks, response_message is what groups errors in a UI.
		Assertions:      &check,
		ResponseMessage: check,
		ResponseCode:    tag(s.Tags, "status"),
		RandomID:        rand.Int32N(1_000_000), //nolint:gosec // matches the JMeter plugin's random_id
	})

	// A failed check fails the transaction containing it, the way a failed
	// JMeter assertion fails its Transaction Controller. The request itself
	// stays successful: nothing ties the check back to it.
	o.markGroupFailure(s.Metadata["vu"], s.Metadata["iter"], segments)
}

// markGroupFailure records that something failed inside a group, for that group
// and every group enclosing it.
//
// With the vu and iter system tags on, a key belongs to one pass through one
// group, so the mark is a verdict: a second failure in the same pass must not
// double it, because the group_duration sample consumes exactly one. Without
// them the key is shared by every concurrent VU in that group, and counting is
// what keeps the failed/total ratio right even though a given failure may land
// on a neighbour's row.
func (o *Output) markGroupFailure(vu, iter string, segments []string) {
	// ponytail: an aborted iteration can leave a group's mark unconsumed. Rather
	// than track iteration ends, drop the lot if it ever grows absurd.
	if len(o.groupFailures) > 100000 {
		o.logger.Warn("dropping unattributed group failure marks")
		clear(o.groupFailures)
	}
	for i := range segments {
		key := groupKey(vu, iter, segments[:i+1])
		if vu != "" {
			o.groupFailures[key] = 1
		} else {
			o.groupFailures[key]++
		}
	}
}

func (o *Output) addTransaction(s metrics.Sample) {
	segments := groupSegments(tag(s.Tags, "group"))
	if len(segments) == 0 {
		return
	}
	key := groupKey(s.Metadata["vu"], s.Metadata["iter"], segments)
	failed := o.groupFailures[key] > 0
	if failed {
		o.groupFailures[key]--
		if o.groupFailures[key] == 0 {
			delete(o.groupFailures, key)
		}
	}

	// Flattening keeps only the outermost group, the way the JMeter plugin keeps
	// only the outermost Transaction Controller. The nested group's failure has
	// already been propagated outwards by addRequest.
	if o.cfg.FlattenNestedTransactions && len(segments) > 1 {
		return
	}
	o.w.addTransaction(transaction{
		Time:            s.Time,
		TransactionName: segments[len(segments)-1],
		Success:         !failed,
		ResponseTime:    int32(s.Value), //nolint:gosec // group_duration is already in ms
	})
}

func trailFailed(tr *httpext.Trail, status, errorMsg string) bool {
	if tr.Failed.Valid {
		return tr.Failed.Bool
	}
	if errorMsg != "" {
		return true
	}
	code, err := strconv.Atoi(status)
	return err != nil || code == 0 || code >= 400
}

func tag(ts *metrics.TagSet, name string) string {
	if ts == nil {
		return ""
	}
	v, _ := ts.Get(name)
	return v
}

// groupSegments splits k6's "::outer::inner" group tag into its parts.
func groupSegments(path string) []string {
	path = strings.TrimPrefix(path, lib.GroupSeparator)
	if path == "" {
		return nil
	}
	return strings.Split(path, lib.GroupSeparator)
}

func groupKey(vu, iter string, segments []string) string {
	return vu + "/" + iter + "/" + strings.Join(segments, lib.GroupSeparator)
}

type pathElement struct {
	Name       string `json:"name"`
	Class      string `json:"class"`
	Occurrence int    `json:"occurrence"`
}

// sourceElementPath is the k6 analogue of the JMeter plugin's test plan path:
// scenario, then each enclosing group, then the request itself. A request that
// names its transaction by tag rather than by group records that instead.
func sourceElementPath(scenario, transaction, controllers string, groups []string, name string) []byte {
	elements := make([]pathElement, 0, len(groups)+4)
	if scenario != "" {
		elements = append(elements, pathElement{Name: scenario, Class: scenarioClass})
	}
	for _, g := range groups {
		elements = append(elements, pathElement{Name: g, Class: groupClass})
	}
	if transaction != "" {
		elements = append(elements, pathElement{Name: transaction, Class: groupClass})
	}
	elements = append(elements, controllerElements(controllers)...)
	elements = append(elements, pathElement{Name: name, Class: requestClass})
	encoded, err := json.Marshal(elements)
	if err != nil {
		return nil
	}
	return encoded
}

// controllerElements parses the controllers tag. A frame is "kind:name"; a
// frame with no recognised kind still becomes a band, just a nameless one.
func controllerElements(tagValue string) []pathElement {
	if tagValue == "" {
		return nil
	}
	frames := strings.Split(tagValue, "/")
	elements := make([]pathElement, 0, len(frames))
	for _, frame := range frames {
		kind, name, ok := strings.Cut(frame, ":")
		if !ok {
			kind, name = "", frame
		}
		if name == "" {
			continue
		}
		class, known := controllerKinds[kind]
		if !known {
			class = controllerClass
		}
		elements = append(elements, pathElement{Name: name, Class: class})
	}
	return elements
}

func ms(d time.Duration) int32 {
	return int32(d.Milliseconds()) //nolint:gosec // response times do not overflow int32
}

func redactedURL(raw string) string {
	at := strings.LastIndex(raw, "@")
	slashes := strings.Index(raw, "//")
	if at < 0 || slashes < 0 || at < slashes {
		return raw
	}
	return raw[:slashes+2] + "***" + raw[at:]
}
