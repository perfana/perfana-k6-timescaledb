//go:build !k6fork

package timescaledb

import (
	"github.com/sirupsen/logrus"
	"go.k6.io/k6/v2/lib"
	"go.k6.io/k6/v2/lib/netext/httpext"
)

// The default build, against upstream k6. An output there receives metric
// samples only: response bodies, headers and the URL of a request that sets a
// name tag never enter the stream, so the columns fed by them stay NULL. Build
// with -tags k6fork against the Breaking-IT fork to fill them (see fork.go).
const buildTarget = "upstream k6"

func errorDetail(_ *httpext.Trail) httpErrorDetail {
	return httpErrorDetail{}
}

func warnMissingCapabilities(logger logrus.FieldLogger, _ lib.Options) {
	logger.Warn("built against upstream k6: requests_error rows carry no headers or body, and a " +
		"request that sets a name tag records that name as its url. Build with -tags k6fork " +
		"against the Breaking-IT k6 fork to record both.")
}
