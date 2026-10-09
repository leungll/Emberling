package greet

import "testing"

func TestGreeting(t *testing.T) {
	if got := Greeting("Ember"); got != "Hello, Ember!" {
		t.Fatalf("Greeting(%q) = %q, want %q", "Ember", got, "Hello, Ember!")
	}
}
