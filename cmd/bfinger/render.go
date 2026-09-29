package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lightwebinc/bcommon/termsafe"
)

// Body values come from a record somebody else wrote and are printed to a
// terminal, which is the original finger's oldest hole: an escape sequence in
// a .plan drove the reader's terminal. So nothing from a record reaches the
// terminal unfiltered. The filter is the library's (package termsafe of
// github.com/lightwebinc/bcommon): printable text and newlines pass; every
// control character, every escape sequence and the bidi and zero-width
// characters that can disguise one string as another are dropped; and the
// output is bounded so a record cannot scroll a terminal indefinitely.
const (
	maxBodyLines = termsafe.MaxLines
	maxBodyCols  = termsafe.MaxCols
)

// renderOpts is how a value is allowed to reach the terminal. ansi keeps
// colour and weight (SGR) sequences, and nothing else, and only when the
// caller has checked the terminal wants them; ascii degrades every rune above
// 0x7E to "?" for a terminal that cannot draw it.
type renderOpts struct {
	ansi  bool
	ascii bool
}

// sanitizeText is sanitize with no options: what pipes and -field get.
func sanitizeText(s string) string { return termsafe.Text(s) }

// sanitize returns s with only printable runes, spaces, tabs and newlines,
// bounded in lines and columns. It never returns a byte sequence a terminal
// interprets, except an SGR sequence when o.ansi is set, which is re-validated
// rather than copied, and is always followed by a reset at the end.
func sanitize(s string, o renderOpts) string {
	return termsafe.Sanitize(s, termsafe.Options{ANSI: o.ansi, ASCII: o.ascii})
}

// localeIsUTF8 reports whether the locale says the terminal draws UTF-8. A
// locale that says nothing is taken as UTF-8: this is the century it is.
func localeIsUTF8() bool { return termsafe.UTF8Locale(os.Getenv) }

// ErrPlanText is the publish-side refusal for a body value a conforming
// reader would have to strip.
var ErrPlanText = errors.New("body text")

// validateBodyText is the owner's side of the bargain for a profile field: a
// value must be printable text, newlines and tabs, with at most SGR escape
// sequences, and bounded like the reader bounds its output. It names the
// first offence.
func validateBodyText(field, s string) error {
	return planText(termsafe.ValidateBounded(field, s))
}

// validateStoreText is the same bargain for store content, without the line
// and column caps. Those two are display bounds, not content rules: they
// exist so a record cannot flood a terminal, and the reader's own sanitiser
// still applies them to whatever it prints. A document published as a store
// is meant to be longer than a screen, and refusing to publish it here would
// enforce a terminal's limits on content that also reaches `-json`, which
// never renders.
func validateStoreText(field, s string) error {
	return planText(termsafe.Validate(field, s))
}

// planText puts ErrPlanText in front of the library's refusal, which begins
// with the field it names, so the text reads "body text: plan line 3 has a
// control character (U+0007)" and matches both sentinels.
func planText(err error) error {
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPlanText, err)
	}
	return nil
}

// safeLabel filters a string that came out of a record before it reaches a
// terminal, with no escape sequence allowed at all. Store names, body field
// names and refusal reasons are publisher-supplied and none of them is ever
// allowed colour, so they go through this rather than through sanitize.
// The VALUES were always filtered; the names and reasons reaching a terminal
// raw was the hole, and it is the same terminal-injection hole the value
// filter exists to close.
func safeLabel(s string) string { return sanitizeText(s) }

// abbrev is the first twelve characters of a key's hex, the form messages
// name a key by. A key read from a file someone else wrote may be shorter
// than that, or not hex at all, so it is cut by rune and filtered like any
// other text from outside.
func abbrev(h string) string { return termsafe.Abbrev(h) }

// renderBody prints one body value under its label. A single-line value sits
// beside the label; a multi-line value starts on the next line, indented, the
// way finger printed a .plan.
func renderBody(w io.Writer, label string, v any, o renderOpts) {
	label = safeLabel(label)
	s := sanitize(fmt.Sprint(v), o)
	if !strings.Contains(s, "\n") {
		fmt.Fprintf(w, "  %-10s %s\n", label, s)
		return
	}
	fmt.Fprintf(w, "  %s\n", label)
	for _, line := range strings.Split(s, "\n") {
		fmt.Fprintf(w, "    %s\n", line)
	}
}
