package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lightwebinc/bcommon/termsafe"
)

// The filter itself is the library's, tested there (package termsafe of
// github.com/lightwebinc/bcommon). What is bfinger's is how it reaches it:
// the reader's options, the store and body bounds, the refusal's words, and
// the layout of a body value.
func TestRenderOptionsReachTheFilter(t *testing.T) {
	in := "caf\u00e9 \x1b[1;31mred\x1b[0m \x1b]0;title\x07\u202eok\t" + strings.Repeat("x", 600)
	for _, o := range []renderOpts{{}, {ansi: true}, {ascii: true}, {ansi: true, ascii: true}} {
		if got, want := sanitize(in, o), termsafe.Sanitize(in, termsafe.Options{ANSI: o.ansi, ASCII: o.ascii}); got != want {
			t.Errorf("%+v: got %q, want %q", o, got, want)
		}
	}
	if sanitizeText(in) != termsafe.Text(in) || safeLabel(in) != termsafe.Text(in) {
		t.Error("the plain filter is not the library's")
	}
	if got := abbrev(strings.Repeat("ab", 33)); got != strings.Repeat("ab", 6) {
		t.Errorf("abbrev: %q", got)
	}
	if maxBodyLines != 200 || maxBodyCols != 512 {
		t.Errorf("display bounds %d x %d, want 200 x 512", maxBodyLines, maxBodyCols)
	}
}

func TestRenderBodyMultiLineIsABlock(t *testing.T) {
	var b bytes.Buffer
	renderBody(&b, "plan", "line one\nline two", renderOpts{})
	want := "  plan\n    line one\n    line two\n"
	if b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
	b.Reset()
	renderBody(&b, "status", "available", renderOpts{})
	if b.String() != "  status     available\n" {
		t.Fatalf("single line: got %q", b.String())
	}
}

func TestValidateBodyTextNamesTheOffence(t *testing.T) {
	ok := []string{"hello\nworld\t:-) https://example.com ☕", "\x1b[32mgreen\x1b[0m", ""}
	for _, s := range ok {
		if err := validateBodyText("plan", s); err != nil {
			t.Errorf("%q refused: %v", s, err)
		}
	}
	bad := map[string]string{
		"a\x07b":                   "control character",
		"\x1b[2Jclear":             "not a colour",
		"\x1b]0;title\x07":         "not a colour",
		"x‮y":                      "bidirectional",
		"bad\xffutf":               "UTF-8",
		strings.Repeat("l\n", 201): "more than 200 lines",
		strings.Repeat("w", 513):   "wider than 512",
	}
	for s, want := range bad {
		err := validateBodyText("plan", s)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want an error mentioning %q", s, err, want)
			continue
		}
		if !strings.HasPrefix(err.Error(), "body text: plan ") || !errors.Is(err, ErrPlanText) {
			t.Errorf("%q: %v is not a body text refusal", s, err)
		}
	}
	// The exact words, and a store's content, which has no display bounds.
	if err := validateBodyText("plan", "a\x07b"); err == nil || err.Error() != "body text: plan line 1 has a control character (U+0007)" {
		t.Errorf("text: %v", err)
	}
	if err := validateStoreText("doc", strings.Repeat("line\n", maxBodyLines*3)); err != nil {
		t.Errorf("a long store was refused: %v", err)
	}
	if err := validateStoreText("doc", "bad\x07bell"); err == nil || err.Error() != "body text: doc line 1 has a control character (U+0007)" {
		t.Errorf("store text: %v", err)
	}
}

// The reader asked for colour, so colour is what they get. NO_COLOR and TERM
// govern whether a program adds colour of its own accord; bfinger never does,
// so neither may veto the flag. Dropping it silently is how a reader is left
// believing the feature does not work.
func TestAnsiFlagIsHonouredWhateverTheEnvironmentSays(t *testing.T) {
	for _, env := range []struct{ term, noColor string }{
		{"xterm-256color", ""}, {"dumb", ""}, {"", ""}, {"xterm-256color", "1"},
	} {
		t.Setenv("TERM", env.term)
		t.Setenv("NO_COLOR", env.noColor)
		rf := &readerFlags{ansi: true}
		if !rf.render(os.Stdout).ansi {
			t.Errorf("TERM=%q NO_COLOR=%q: -ansi was dropped", env.term, env.noColor)
		}
		if (&readerFlags{}).render(os.Stdout).ansi {
			t.Errorf("TERM=%q: colour without the flag", env.term)
		}
	}
}
