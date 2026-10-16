package timescaledb

import (
	"crypto/md5" //nolint:gosec // not a security hash: must match the JMeter plugin's url_hash
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Port of the JMeter plugin's UrlNormalizer. The hash must stay byte-identical
// with it: both plugins share the url_patterns table.
var (
	uuidPattern      = regexp.MustCompile(`[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}`)
	numericIDPattern = regexp.MustCompile(`^\d+$`)
	longTokenPattern = regexp.MustCompile(`^[a-zA-Z0-9]{21,}$`)
)

// A segment mixing letters and digits is an identifier (Siebel's 1-q02oj1h);
// eight characters keeps path words like v2, oauth2, base64, sha256.
const minMixedIDLength = 8

type normalizedURL struct {
	normalized string
	hash       string
	original   string
}

func normalizeURL(url string) normalizedURL {
	if url == "" {
		return normalizedURL{normalized: "", hash: md5Hex(""), original: url}
	}
	n := normalizePathAndQuery(url)
	return normalizedURL{normalized: n, hash: md5Hex(n), original: url}
}

func normalizePathAndQuery(url string) string {
	path, query, hasQuery := strings.Cut(url, "?")
	normalized := normalizePath(path)
	if hasQuery && query != "" {
		return normalized + "?" + normalizeQuery(query)
	}
	return normalized
}

// Only the path is examined: classifying the host would mask a farm node such
// as web3app0110.example.com and collapse every node into one pattern.
func normalizePath(url string) string {
	start := 0
	if i := strings.Index(url, "://"); i >= 0 {
		start = len(url)
		if j := strings.IndexByte(url[i+3:], '/'); j >= 0 {
			start = i + 3 + j
		}
	}
	segments := strings.Split(uuidPattern.ReplaceAllString(url[start:], "{uuid}"), "/")
	for i, segment := range segments {
		// ;jsessionid=... belongs to the segment: mask its value, classify the rest.
		params := ""
		if semi := strings.IndexByte(segment, ';'); semi >= 0 {
			params = normalizePathParams(segment[semi:])
			segment = segment[:semi]
		}
		segments[i] = classifySegment(segment) + params
	}
	return url[:start] + strings.Join(segments, "/")
}

func normalizePathParams(params string) string {
	var b strings.Builder
	for _, p := range strings.Split(params, ";") {
		if p == "" {
			continue
		}
		b.WriteByte(';')
		if name, _, ok := strings.Cut(p, "="); ok {
			b.WriteString(name + "={val}")
		} else {
			b.WriteString(p)
		}
	}
	return b.String()
}

func classifySegment(segment string) string {
	switch {
	case segment == "":
	case strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}"):
	case numericIDPattern.MatchString(segment):
		return "{id}"
	// Before the mixed-id rule, so existing {token} hashes do not move.
	case longTokenPattern.MatchString(segment):
		return "{token}"
	case isMixedID(segment):
		return "{id}"
	}
	return segment
}

func isMixedID(segment string) bool {
	// ponytail: rune count matches Java's String.length() for BMP chars only; fine for URL paths
	if len([]rune(segment)) < minMixedIDLength {
		return false
	}
	letter, digit := false, false
	for _, r := range segment {
		switch {
		case unicode.IsLetter(r):
			letter = true
		case unicode.IsDigit(r):
			digit = true
		case r != '-' && r != '_' && r != '.':
			return false
		}
	}
	return letter && digit
}

func normalizeQuery(query string) string {
	params := strings.Split(query, "&")
	for i, param := range params {
		if name, _, ok := strings.Cut(param, "="); ok {
			params[i] = name + "={val}"
		}
	}
	sort.Strings(params)
	return strings.Join(params, "&")
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // see above
	return hex.EncodeToString(sum[:])
}
