package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// detailedError stands for an Adapter error that keeps its raw transport text out of
// Error and exposes it only as developer detail.
type detailedError struct{ message, detail string }

func (e *detailedError) Error() string  { return e.message }
func (e *detailedError) Detail() string { return e.detail }

func TestBoundedExecutionError_OversizedMessage_PersistsAtMostTheBound(t *testing.T) {
	// A multi-byte rune straddles the cut so the truncation must back off to a rune start.
	long := strings.Repeat("é", 5_000)
	got := boundedExecutionError("EXECUTOR_ERROR", errors.New(long))

	if got.Code != "EXECUTOR_ERROR" {
		t.Errorf("Code = %q, want EXECUTOR_ERROR", got.Code)
	}
	if len(got.Message) > maxExecutionErrorMessageBytes {
		t.Errorf("Message is %d bytes, want at most %d", len(got.Message), maxExecutionErrorMessageBytes)
	}
	if !utf8.ValidString(got.Message) {
		t.Errorf("Message is not valid UTF-8 after truncation")
	}
	if !strings.HasSuffix(got.Message, "…") {
		t.Errorf("Message %q does not mark its truncation", got.Message[len(got.Message)-8:])
	}
}

func TestBoundedExecutionError_TenThousandCharacterMessage_PersistsAtMostTheBound(t *testing.T) {
	got := boundedExecutionError("TOOL_ERROR", errors.New(strings.Repeat("x", 10_000)))
	if len(got.Message) > maxExecutionErrorMessageBytes {
		t.Errorf("Message is %d bytes, want at most %d", len(got.Message), maxExecutionErrorMessageBytes)
	}
}

func TestBoundedExecutionError_ShortMessage_KeptVerbatim(t *testing.T) {
	const message = `image_generation: dispatch: mocktask: dispatch to provider "mock-task-provider-v1": send request: provider request timed out before the deadline`
	got := boundedExecutionError("EXECUTOR_ERROR", errors.New(message))
	if got.Message != message {
		t.Errorf("Message = %q, want it unchanged %q", got.Message, message)
	}
}

func TestExecutionFailureLogDetail_URLsInChainAndDetail_StripsQueryFragmentAndUserinfo(t *testing.T) {
	raw := `Post "http://user:pa55@mockprovider:9101/v1/tasks?key=s3cr3t#frag": dial tcp 10.0.0.7:9101: connect: connection refused`
	err := fmt.Errorf("image_generation: dispatch: %w", &detailedError{
		message: `mocktask: send request (seen at https://admin:hunter2@llm.example/v1?token=abc&x=1): provider request failed: transport error`,
		detail:  raw,
	})

	got := executionFailureLogDetail(err)

	for _, leak := range []string{"s3cr3t", "key=", "#frag", "frag", "user:", "pa55", "admin", "hunter2", "token=abc", "x=1"} {
		if strings.Contains(got, leak) {
			t.Errorf("log detail %q carries %q", got, leak)
		}
	}
	for _, kept := range []string{
		"image_generation: dispatch:",
		"provider request failed: transport error",
		"http://mockprovider:9101/v1/tasks",
		"https://llm.example/v1",
		"connection refused",
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("log detail %q lacks %q", got, kept)
		}
	}
}

func TestExecutionFailureLogDetail_NoDetailInChain_IsTheRedactedErrorText(t *testing.T) {
	got := executionFailureLogDetail(errors.New(`send request: Post "https://llm.example/v1/chat?key=s3cr3t": EOF`))
	if want := `send request: Post "https://llm.example/v1/chat": EOF`; got != want {
		t.Errorf("log detail = %q, want %q", got, want)
	}
}
