package timescaledb

import (
	"github.com/sirupsen/logrus"
	"go.k6.io/k6/v2/lib"
	"go.k6.io/k6/v2/lib/netext/httpext"
	"go.k6.io/k6/v2/metrics"
)

// This branch builds against the Breaking-IT k6 fork only (see the replace in
// go.mod), which puts request and response detail for failed requests into the
// metric stream. The symbols below do not exist upstream; the main branch keeps
// an upstream build.
const buildTarget = "Breaking-IT k6 fork"

// errorDetail pulls the detail the fork attaches to the http_req_failed sample
// of a failed request. Empty when the httpErrorData option is off.
func errorDetail(tr *httpext.Trail) httpErrorDetail {
	for _, s := range tr.Samples {
		if s.Metric.Name == metrics.HTTPReqFailedName {
			return httpErrorDetail{s.HTTPErrorReqHeaders, s.HTTPErrorReqBody, s.HTTPErrorResHeaders, s.HTTPErrorResBody}
		}
	}
	return httpErrorDetail{}
}

// warnMissingCapabilities reports the fork options this output needs but the
// test did not switch on, since each one silently empties columns.
func warnMissingCapabilities(logger logrus.FieldLogger, opts lib.Options) {
	if !opts.HTTPErrorData.Bool {
		logger.Warn("httpErrorData is off, so requests_error rows will have no headers or body; " +
			"enable it with K6_HTTP_ERROR_DATA=true or options.httpErrorData = true")
	}
	// ponytail: httpRequestUrl (K6_HTTP_REQUEST_URL) is not in the published fork
	// yet; request_url metadata is read when a fork build sets it. Warn here once
	// the option ships.
}
