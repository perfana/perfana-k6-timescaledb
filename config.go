package timescaledb

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Config mirrors the JMeter backend listener parameters, minus the ones JMeter
// needs only because it builds a JDBC URL out of parts: host/port/database/user/
// password/sslMode all live in the single postgres DSN.
type Config struct {
	URL         string
	Schema      string
	MaxPoolSize int

	BatchSize     int
	FlushInterval time.Duration

	RunID           string
	SystemUnderTest string
	TestEnvironment string
	ScenarioName    string
	Location        string
	NodeName        string

	SyntheticMonitoring       bool
	SaveResponseBody          bool
	NormalizeURLs             bool
	FlattenNestedTransactions bool
	SaveFailedChecks          bool
}

func defaultConfig() Config {
	return Config{
		URL:                       "postgres://localhost:5432/k6?sslmode=prefer",
		Schema:                    "public",
		MaxPoolSize:               10,
		BatchSize:                 1000,
		FlushInterval:             time.Second,
		RunID:                     "Run",
		SystemUnderTest:           "SUT",
		TestEnvironment:           "test",
		ScenarioName:              "Scenario",
		Location:                  "local",
		NodeName:                  "controller",
		SaveResponseBody:          true,
		NormalizeURLs:             true,
		FlattenNestedTransactions: true,
		SaveFailedChecks:          true,
	}
}

// EffectiveRunID matches the JMeter listener: synthetic monitoring keys rows by
// scenario instead of by run.
func (c Config) EffectiveRunID() string {
	if c.SyntheticMonitoring {
		return c.ScenarioName
	}
	return c.RunID
}

// ParseConfig applies, in order: defaults, K6_TIMESCALEDB_<KEY> environment
// variables, then the `--out perfana-timescaledb=k=v,k=v` argument.
func ParseConfig(arg string, env map[string]string) (Config, error) {
	cfg := defaultConfig()

	for key := range setters {
		if v, ok := env["K6_TIMESCALEDB_"+envName(key)]; ok {
			if err := apply(&cfg, key, v); err != nil {
				return cfg, err
			}
		}
	}

	for _, pair := range strings.Split(arg, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return cfg, fmt.Errorf("malformed output argument %q, expected key=value", pair)
		}
		if err := apply(&cfg, strings.TrimSpace(key), value); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

// envName turns "flushInterval" into "FLUSH_INTERVAL".
func envName(key string) string {
	var b strings.Builder
	for i, r := range key {
		if r >= 'A' && r <= 'Z' && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

//nolint:gochecknoglobals // the key->field table is the config schema
var setters = map[string]func(*Config, string) error{
	"url":         func(c *Config, v string) error { c.URL = v; return nil },
	"schema":      func(c *Config, v string) error { c.Schema = v; return nil },
	"maxPoolSize": func(c *Config, v string) error { return setInt(&c.MaxPoolSize, v) },
	"batchSize":   func(c *Config, v string) error { return setInt(&c.BatchSize, v) },
	"flushInterval": func(c *Config, v string) error {
		// Bare numbers are seconds, for parity with the JMeter parameter.
		if secs, err := strconv.Atoi(v); err == nil {
			c.FlushInterval = time.Duration(secs) * time.Second
			return nil
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("flushInterval: %w", err)
		}
		c.FlushInterval = d
		return nil
	},
	"runId":                     func(c *Config, v string) error { c.RunID = v; return nil },
	"systemUnderTest":           func(c *Config, v string) error { c.SystemUnderTest = v; return nil },
	"testEnvironment":           func(c *Config, v string) error { c.TestEnvironment = v; return nil },
	"scenarioName":              func(c *Config, v string) error { c.ScenarioName = v; return nil },
	"location":                  func(c *Config, v string) error { c.Location = v; return nil },
	"nodeName":                  func(c *Config, v string) error { c.NodeName = v; return nil },
	"syntheticMonitoring":       func(c *Config, v string) error { return setBool(&c.SyntheticMonitoring, v) },
	"saveResponseBody":          func(c *Config, v string) error { return setBool(&c.SaveResponseBody, v) },
	"normalizeUrls":             func(c *Config, v string) error { return setBool(&c.NormalizeURLs, v) },
	"flattenNestedTransactions": func(c *Config, v string) error { return setBool(&c.FlattenNestedTransactions, v) },
	"saveFailedChecks":          func(c *Config, v string) error { return setBool(&c.SaveFailedChecks, v) },
}

func apply(cfg *Config, key, value string) error {
	set, ok := setters[key]
	if !ok {
		return fmt.Errorf("unknown option %q", key)
	}
	return set(cfg, strings.TrimSpace(value))
}

func setInt(dst *int, v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	*dst = n
	return nil
}

func setBool(dst *bool, v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	*dst = b
	return nil
}
