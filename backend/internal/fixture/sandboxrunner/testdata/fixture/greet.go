// Package greet is the sandbox runner's fixture repository: a stdlib-only module with one
// function and one test, at the base commit fixture-v1.
package greet

// Greeting returns the greeting for name.
func Greeting(name string) string {
	return "Hello, " + name + "!"
}
