//go:build k6fork

package timescaledb

import (
	"github.com/sirupsen/logrus"
	"go.k6.io/k6/v2/lib"
	"go.k6.io/k6/v2/lib/netext/httpext"
	"go.k6.io/k6/v2/metrics"
)

// Built with -tags k6fork, against the Breaking-IT k6 fork, which puts request
// and response detail for failed requests into the metric stream and keeps the
// URL of a named request in metadata. Neither exists upstream, so the symbols
// below only compile here; upstream.go is the other half.
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
	// Without it, a script that names its requests leaves k6 with the name in the
	// url tag and the URL nowhere, so url_patterns records sampler names.
	if !opts.HTTPRequestURL.Bool {
		logger.Warn("httpRequestUrl is off, so requests that set a name tag record that name as " +
			"their url; enable it with K6_HTTP_REQUEST_URL=true or options.httpRequestUrl = true")
	}
}
