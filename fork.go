package timescaledb

import (
	"github.com/sirupsen/logrus"
	"go.k6.io/k6/v2/lib"
	"go.k6.io/k6/v2/lib/netext/httpext"
	"go.k6.io/k6/v2/metrics"
)

// This branch builds against the perfana k6 fork only (see the replace in
// go.mod), which puts request and response detail for failed requests into the
// metric stream. The symbols below do not exist upstream; the main branch keeps
// an upstream build.
const buildTarget = "perfana k6 fork"

// errorDetail pulls the detail the fork attaches to the metadata of the
// http_req_failed sample of a failed request (never to tr.Metadata). Empty when
// the httpErrorData option is off.
func errorDetail(tr *httpext.Trail) httpErrorDetail {
	for _, s := range tr.Samples {
		if s.Metric.Name == metrics.HTTPReqFailedName || s.Metric.Name == browserReqFailed {
			return httpErrorDetail{
				s.Metadata[httpext.MetadataErrorReqHeaders], s.Metadata[httpext.MetadataErrorReqBody],
				s.Metadata[httpext.MetadataErrorResHeaders], s.Metadata[httpext.MetadataErrorResBody],
			}
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
	if !opts.HTTPRequestURL.Bool {
		logger.Warn("httpRequestUrl is off, so requests with a name tag record that name in url_patterns; " +
			"enable it with K6_HTTP_REQUEST_URL=true or options.httpRequestUrl = true")
	}
}
