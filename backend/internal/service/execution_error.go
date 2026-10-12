package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// maxExecutionErrorMessageBytes bounds the message of an ExecutionError built from an
// executor, model or Tool error. That message is persisted and then copied into Events,
// Trace and API responses, so it stays a short summary however long the error chain is.
const maxExecutionErrorMessageBytes = 512

// maxExecutionFailureLogBytes bounds the developer detail logged for one such failure.
const maxExecutionFailureLogBytes = 4096

// truncationMark ends a message that was cut to its bound.
const truncationMark = "…"

// urlPattern matches one URL-looking substring: a scheme, "://" and everything up to the
// next whitespace or quoting character.
var urlPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.\-]*://[^\s"'<>]+`)

// boundedExecutionError is the persisted summary of a failed executor, model or Tool
// call. The Adapters already keep Provider hosts, URLs and transport text out of their
// error text; the bound is the second line of defence, so no error chain, however it was
// built, persists more than maxExecutionErrorMessageBytes.
func boundedExecutionError(code string, err error) domain.ExecutionError {
	return domain.ExecutionError{Code: code, Message: truncateUTF8(err.Error(), maxExecutionErrorMessageBytes)}
}

// executionFailureLogDetail is the developer log text of a failed executor, model or Tool
// call: the full error chain plus the Detail of the error in it that keeps raw transport
// text out of its own message. Every URL in it loses its userinfo, query string and
// fragment, where a Provider credential could travel, and the result is bounded.
func executionFailureLogDetail(err error) string {
	text := err.Error()
	var detailed interface{ Detail() string }
	if errors.As(err, &detailed) {
		if detail := detailed.Detail(); detail != "" {
			text += " (detail: " + detail + ")"
		}
	}
	return truncateUTF8(urlPattern.ReplaceAllStringFunc(text, redactURL), maxExecutionFailureLogBytes)
}

// logExecutionFailure records a failed executor, model or Tool call once, at Warn, with
// the stable identifiers in attrs and the redacted developer detail. The persisted
// ExecutionError never carries that detail.
func (s *ExecutionService) logExecutionFailure(ctx context.Context, message, code string, err error, attrs ...any) {
	attrs = append(attrs, "code", code, "detail", executionFailureLogDetail(err))
	s.deps.Logger.WarnContext(ctx, message, attrs...)
}

// redactURL keeps a URL's scheme, host and path and drops its userinfo, query string and
// fragment. It works on the text alone, so a URL that would not parse is redacted too.
func redactURL(raw string) string {
	schemeEnd := strings.Index(raw, "://") + len("://")
	rest := raw[schemeEnd:]
	if cut := strings.IndexAny(rest, "?#"); cut >= 0 {
		rest = rest[:cut]
	}
	authorityEnd := strings.IndexByte(rest, '/')
	if authorityEnd < 0 {
		authorityEnd = len(rest)
	}
	if at := strings.LastIndexByte(rest[:authorityEnd], '@'); at >= 0 {
		rest = rest[at+1:]
	}
	return raw[:schemeEnd] + rest
}

// truncateUTF8 cuts s to at most limit bytes on a rune boundary, ending it with
// truncationMark when anything was cut.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit - len(truncationMark)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncationMark
}
