package timescaledb

import (
	"time"

	"go.k6.io/k6/v2/lib/netext/httpext"
	"go.k6.io/k6/v2/metrics"
	"gopkg.in/guregu/null.v3"
)

// k6 reports every network request a browser page makes under browser_* names.
// Stock k6 (and the perfana fork) sends only duration, bytes received and the
// failed verdict; the Breaking-IT fork adds the HTTP phases, which then fill
// connect and latency.
const (
	browserReqDuration       = "browser_http_req_duration"
	browserReqConnecting     = "browser_http_req_connecting"
	browserReqTLSHandshaking = "browser_http_req_tls_handshaking"
	browserReqSending        = "browser_http_req_sending"
	browserReqWaiting        = "browser_http_req_waiting"
	browserReqReceiving      = "browser_http_req_receiving"
	browserReqFailed         = "browser_http_req_failed"
	browserDataReceived      = "browser_data_received"
)

// browserKey pairs a request's timing bundle with its browser_http_req_failed
// sample, which k6 pushes as a separate bundle right after it. Both share
// the tag set (interned, so the pointer is identity) and the timestamp.
type browserKey struct {
	tags *metrics.TagSet
	at   int64
}

type browserRequest struct {
	trail        *httpext.Trail
	responseSize *int32
	// flushes counts the flushes this request has waited for its failed sample.
	flushes int
}

// addBrowserRequest handles a container holding browser request samples and
// reports whether it did. A request whose failed sample has not arrived yet is
// parked until it does, or until the next flush ends without it.
func (o *Output) addBrowserRequest(samples []metrics.Sample) bool {
	if len(samples) > 0 && tag(samples[0].Tags, "from_cache") == "true" {
		// Served from the browser cache, not the network. The Breaking-IT fork
		// never emits these; stock k6 does. Its failed sample finds nothing parked.
		return isBrowserRequest(samples[0].Metric.Name)
	}
	var req *browserRequest
	var failed *metrics.Sample
	for i := range samples {
		s := &samples[i]
		if s.Metric.Name == browserReqFailed {
			failed = s
			continue
		}
		if !isBrowserRequest(s.Metric.Name) {
			continue
		}
		if req == nil {
			req = &browserRequest{trail: &httpext.Trail{EndTime: s.Time, Tags: s.Tags, Metadata: s.Metadata}}
		}
		req.set(s)
	}
	if req == nil && failed == nil {
		return false
	}

	if req == nil {
		// The failed half of a request whose timings came in an earlier bundle.
		key := browserKey{failed.Tags, failed.Time.UnixNano()}
		if req = o.pendingBrowser[key]; req == nil {
			return true // its timings were already written without it
		}
		delete(o.pendingBrowser, key)
	}
	if failed != nil {
		req.trail.Failed = null.BoolFrom(failed.Value != 0)
		req.trail.Samples = append(req.trail.Samples, *failed)
		o.addRequest(req.trail, true, req.responseSize)
		return true
	}
	o.pendingBrowser[browserKey{req.trail.Tags, req.trail.EndTime.UnixNano()}] = req
	return true
}

func isBrowserRequest(name string) bool {
	switch name {
	case browserReqDuration, browserReqConnecting, browserReqTLSHandshaking, browserReqSending,
		browserReqWaiting, browserReqReceiving, browserReqFailed, browserDataReceived:
		return true
	}
	return false
}

func (r *browserRequest) set(s *metrics.Sample) {
	tr := r.trail
	d := time.Duration(s.Value * float64(time.Millisecond))
	switch s.Metric.Name {
	case browserReqDuration:
		tr.Duration = d
	case browserReqConnecting:
		tr.Connecting = d
	case browserReqTLSHandshaking:
		tr.TLSHandshaking = d
	case browserReqSending:
		tr.Sending = d
	case browserReqWaiting:
		tr.Waiting = d
	case browserReqReceiving:
		tr.Receiving = d
	case browserDataReceived:
		size := int32(s.Value) //nolint:gosec // one response is far below 2 GiB
		r.responseSize = &size
	}
	tr.ConnDuration = tr.Connecting + tr.TLSHandshaking
}

// flushPendingBrowser writes the parked requests that have already waited one
// whole flush (all of them when final), judging success from the status tag as
// the HTTP path does when a trail has no verdict. k6 sends no failed sample
// when Chromium never reported a response.
func (o *Output) flushPendingBrowser(final bool) {
	for key, req := range o.pendingBrowser {
		if req.flushes == 0 && !final {
			req.flushes++
			continue
		}
		delete(o.pendingBrowser, key)
		o.addRequest(req.trail, true, req.responseSize)
	}
}
