package main

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lightwebinc/bcommon/resolve"
	"github.com/lightwebinc/bfinger/internal/config"
	"github.com/lightwebinc/bfinger/internal/goldentest"
)

// The refusal texts of the reader's network packages (resolve, hostset,
// lookup, headers), pinned whole as bfinger prints them. Those packages are
// shared code, and their own tests match a word or two of each message
// because they cannot know how an application words the wrapping. What a
// user reads is the whole line: bfinger's wrap around the package's text.
// Every row here is driven through bfinger's own call path (the lookup
// command, doctor, pay's recipient resolution), so a reworded message
// anywhere in that chain, or a wrap dropped or doubled, fails here.
//
// Where a message embeds a standard-library error (a JSON syntax error, a
// hex error, the url.Error the HTTP client wraps around a refused redirect),
// the test builds that part with the standard library and pins everything
// around it.

// readerRun runs bfinger as a user would, with a fresh home and no config
// file, and returns the exit code and both streams.
func readerRun(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BFINGER_HOME", home)
	stdout, readOut := tempFile(t)
	stderr, readErr := tempFile(t)
	all := append([]string{"-home", home, "-known-keys", filepath.Join(home, "known_keys"), "-timeout", "5s"}, args...)
	code := run(all, stdout, stderr)
	return code, readOut(), readErr()
}

// jsonErr is the standard library's text for body as JSON.
func jsonErr(t *testing.T, body string) string {
	t.Helper()
	var v any
	err := json.Unmarshal([]byte(body), &v)
	if err == nil {
		t.Fatalf("%q parses as JSON", body)
	}
	return err.Error()
}

// goldenIdentity is the golden vector's identity key, hex: a point on the
// curve, so bfinger takes it as an address and goes on to the host.
func goldenIdentity(t *testing.T) string {
	t.Helper()
	return goldentest.Load(t).IdentityKeyHex
}

// lookupHost is an overlay host that answers every request with h, and its
// base URL and the ip:port hostset dials for it.
func lookupHost(t *testing.T, h http.HandlerFunc) (base, addr string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL, srv.Listener.Addr().String()
}

func answer(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// oversized answers a 200 of n bytes, written in pieces so the test does not
// hold the whole body.
func oversized(n int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		piece := []byte(strings.Repeat("x", 1<<20))
		for left := n; left > 0; left -= len(piece) {
			if left < len(piece) {
				piece = piece[:left]
			}
			if _, err := w.Write(piece); err != nil {
				return // the reader stopped at its bound
			}
		}
	}
}

// counted counts the requests h is asked, so a row can pin how many hops a
// redirect bound lets through and not only the words it refuses with.
func counted(n *atomic.Int32, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h(w, r)
	}
}

// TestLookupRefusalTextsAreFrozen pins hostset's and lookup's refusals as
// the lookup command prints them, with the identity key as the address so
// no name resolution runs.
func TestLookupRefusalTextsAreFrozen(t *testing.T) {
	id := goldenIdentity(t)
	long := strings.Repeat("x", 250)
	// atBound is the JSON syntax error for the body oversized writes at
	// exactly hostset's bound.
	atBound := jsonErr(t, strings.Repeat("x", 64<<20))

	for _, c := range []struct {
		name   string
		quorum string
		host   http.HandlerFunc
		// want is the stderr line after the host's base (B) and its dialed
		// address (A) are filled in.
		want func(base, addr string) string
	}{
		{"quorum beyond the hosts", "2", answer(200, `{"type":"output-list","outputs":[]}`),
			func(b, _ string) string {
				return fmt.Sprintf("lookup at %[1]s: hostset: %[1]s: quorum 2 exceeds the 1 host(s) the name resolves to", b)
			}},
		{"quorum shortfall on a 5xx", "1", answer(503, "down"),
			func(b, a string) string {
				return fmt.Sprintf("lookup at %[1]s: hostset: %[1]s: 0 of 1 host(s) answered, need 1: 127.0.0.1@%[2]s: status 503", b, a)
			}},
		// The lowest status hostset counts as a failure, not an answer.
		{"quorum shortfall on a 500", "1", answer(500, "down"),
			func(b, a string) string {
				return fmt.Sprintf("lookup at %[1]s: hostset: %[1]s: 0 of 1 host(s) answered, need 1: 127.0.0.1@%[2]s: status 500", b, a)
			}},
		// Only a 200 is an answer: another 2xx is refused with its status,
		// not parsed.
		{"host answered a 204", "1", answer(204, ""),
			func(b, a string) string {
				return fmt.Sprintf("lookup at %s: lookup: 127.0.0.1@%s answered status 204: ", b, a)
			}},
		// hostset's default bound, one byte over: the default and the text
		// are both what a user meets, and neither is set by bfinger.
		{"answer over the body bound", "1", oversized(64<<20 + 1),
			func(b, a string) string {
				return fmt.Sprintf("lookup at %[1]s: hostset: %[1]s: 0 of 1 host(s) answered, need 1: 127.0.0.1@%[2]s: hostset: response body exceeds the bound (67108864 bytes)", b, a)
			}},
		// Exactly at the bound the body is read whole and goes on to be
		// parsed, so the refusal is lookup's, not hostset's.
		{"answer at the body bound", "1", oversized(64 << 20),
			func(b, a string) string {
				return fmt.Sprintf("lookup at %s: lookup: 127.0.0.1@%s: answer is not the expected JSON: %s", b, a, atBound)
			}},
		{"host answered a 4xx", "1", answer(400, ` {"status":"error","description":"unknown service"}`+"\n"),
			func(b, a string) string {
				return fmt.Sprintf(`lookup at %s: lookup: 127.0.0.1@%s answered status 400: {"status":"error","description":"unknown service"}`, b, a)
			}},
		{"a 4xx body is cut at 200 bytes", "1", answer(404, long),
			func(b, a string) string {
				return fmt.Sprintf("lookup at %s: lookup: 127.0.0.1@%s answered status 404: %s...", b, a, long[:200])
			}},
		// A body of exactly 200 bytes is shown whole, with no ellipsis.
		{"a 4xx body of 200 bytes is whole", "1", answer(404, long[:200]),
			func(b, a string) string {
				return fmt.Sprintf("lookup at %s: lookup: 127.0.0.1@%s answered status 404: %s", b, a, long[:200])
			}},
		// An answer with no type is not an output-list either.
		{"answer with no type", "1", answer(200, `{"outputs":[]}`),
			func(b, a string) string {
				return fmt.Sprintf(`lookup at %s: lookup: answer is not an output-list: 127.0.0.1@%s answered type ""`, b, a)
			}},
		{"answer is not JSON", "1", answer(200, "not json"),
			func(b, a string) string {
				return fmt.Sprintf("lookup at %s: lookup: 127.0.0.1@%s: answer is not the expected JSON: %s", b, a, jsonErr(t, "not json"))
			}},
		{"answer is not an output-list", "1", answer(200, `{"type":"freeform","result":{}}`),
			func(b, a string) string {
				return fmt.Sprintf(`lookup at %s: lookup: answer is not an output-list: 127.0.0.1@%s answered type "freeform"`, b, a)
			}},
		{"beef element is not a byte", "1", answer(200, `{"type":"output-list","outputs":[{"beef":[256],"outputIndex":0}]}`),
			func(b, a string) string {
				return fmt.Sprintf("lookup at %s: lookup: 127.0.0.1@%s: answer is not the expected JSON: beef: element 0 is 256, not a byte", b, a)
			}},
		{"beef is neither form", "1", answer(200, `{"type":"output-list","outputs":[{"beef":{"x":1},"outputIndex":0}]}`),
			func(b, a string) string {
				return fmt.Sprintf("lookup at %s: lookup: 127.0.0.1@%s: answer is not the expected JSON: beef: neither an array of bytes nor a base64 string", b, a)
			}},
		{"context element is not a byte", "1", answer(200, `{"type":"output-list","outputs":[{"beef":[1],"outputIndex":0,"context":[-1]}]}`),
			func(b, a string) string {
				return fmt.Sprintf("lookup at %s: lookup: 127.0.0.1@%s: answer is not the expected JSON: context: element 0 is -1, not a byte", b, a)
			}},
		{"beef before context", "1", answer(200, `{"type":"output-list","outputs":[{"beef":[256],"outputIndex":0,"context":[-1]}]}`),
			func(b, a string) string {
				return fmt.Sprintf("lookup at %s: lookup: 127.0.0.1@%s: answer is not the expected JSON: beef: element 0 is 256, not a byte", b, a)
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, addr := lookupHost(t, c.host)
			code, _, stderr := readerRun(t, "-host", base, "-header-url", "http://127.0.0.1:1", "-quorum", c.quorum, id)
			if want := "bfinger: " + c.want(base, addr) + "\n"; stderr != want || code != 2 {
				t.Fatalf("exit %d, stderr\n%q\nwant exit 2 and\n%q", code, stderr, want)
			}
		})
	}
}

// TestHostBaseTextsAreFrozen pins hostset's refusals of the configured base
// URL itself, before anything is dialed. The last row breaks both rules and
// pins that the scheme is checked before the host.
func TestHostBaseTextsAreFrozen(t *testing.T) {
	id := goldenIdentity(t)
	for _, c := range []struct{ host, want string }{
		{"ftp://overlay.example", `lookup at ftp://overlay.example: hostset: ftp://overlay.example: base "ftp://overlay.example": scheme "ftp" is not http or https`},
		{"http://", `lookup at http://: hostset: http://: base "http://" has no host`},
		{"ftp://", `lookup at ftp://: hostset: ftp://: base "ftp://": scheme "ftp" is not http or https`},
	} {
		code, _, stderr := readerRun(t, "-host", c.host, "-header-url", "http://127.0.0.1:1", "-quorum", "1", id)
		if want := "bfinger: " + c.want + "\n"; stderr != want || code != 2 {
			t.Errorf("%s: exit %d, stderr\n%q\nwant exit 2 and\n%q", c.host, code, stderr, want)
		}
	}
}

// TestHostRedirectTextsAreFrozen pins hostset's two redirect refusals. The
// HTTP client wraps them in a url.Error naming the request, which is built
// here from the standard library. hops is how many requests the host is
// asked before the refusal: a bound moved by one changes it and not the text.
func TestHostRedirectTextsAreFrozen(t *testing.T) {
	id := goldenIdentity(t)
	for _, c := range []struct {
		name string
		host func(self *string) http.HandlerFunc
		hops int32
		want func(base, addr string) string
	}{
		{"to another origin",
			func(*string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, "http://other.example/lookup", http.StatusFound)
				}
			},
			1,
			func(b, a string) string {
				ue := &url.Error{Op: "Post", URL: "http://other.example/lookup",
					Err: errors.New("hostset: redirect to another origin refused: http://" + a + " -> http://other.example")}
				return fmt.Sprintf("lookup at %[1]s: hostset: %[1]s: 0 of 1 host(s) answered, need 1: 127.0.0.1@%[2]s: %[3]s", b, a, ue)
			}},
		// The origin is the scheme as well as the host: the same host
		// under another scheme is refused in the same words.
		{"to another scheme",
			func(self *string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, "https://"+strings.TrimPrefix(*self, "http://")+"/lookup", http.StatusFound)
				}
			},
			1,
			func(b, a string) string {
				ue := &url.Error{Op: "Post", URL: "https://" + a + "/lookup",
					Err: errors.New("hostset: redirect to another origin refused: http://" + a + " -> https://" + a)}
				return fmt.Sprintf("lookup at %[1]s: hostset: %[1]s: 0 of 1 host(s) answered, need 1: 127.0.0.1@%[2]s: %[3]s", b, a, ue)
			}},
		{"more than five",
			func(self *string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, *self+"/lookup", http.StatusFound)
				}
			},
			5,
			func(b, a string) string {
				ue := &url.Error{Op: "Post", URL: b + "/lookup", Err: errors.New("hostset: more than 5 redirects")}
				return fmt.Sprintf("lookup at %[1]s: hostset: %[1]s: 0 of 1 host(s) answered, need 1: 127.0.0.1@%[2]s: %[3]s", b, a, ue)
			}},
		// The count is checked before the origin: a sixth hop that leaves
		// the origin is refused for the length of the chain.
		{"five within the origin, then out of it",
			func(self *string) http.HandlerFunc {
				var n atomic.Int32
				return func(w http.ResponseWriter, r *http.Request) {
					to := *self + "/lookup"
					if n.Add(1) == 5 {
						to = "http://other.example/lookup"
					}
					http.Redirect(w, r, to, http.StatusFound)
				}
			},
			5,
			func(b, a string) string {
				ue := &url.Error{Op: "Post", URL: "http://other.example/lookup", Err: errors.New("hostset: more than 5 redirects")}
				return fmt.Sprintf("lookup at %[1]s: hostset: %[1]s: 0 of 1 host(s) answered, need 1: 127.0.0.1@%[2]s: %[3]s", b, a, ue)
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var self string
			var hops atomic.Int32
			base, addr := lookupHost(t, counted(&hops, c.host(&self)))
			self = base
			code, _, stderr := readerRun(t, "-host", base, "-header-url", "http://127.0.0.1:1", "-quorum", "1", id)
			if want := "bfinger: " + c.want(base, addr) + "\n"; stderr != want || code != 2 {
				t.Fatalf("exit %d, stderr\n%q\nwant exit 2 and\n%q", code, stderr, want)
			}
			if got := hops.Load(); got != c.hops {
				t.Fatalf("the host was asked %d time(s), want %d", got, c.hops)
			}
		})
	}
}

// TestHostReadTakesTheTimeout pins that -timeout bounds each host read, not
// only discovery. The host here holds the question for five seconds: at
// -timeout 300ms the read is given up on and the host named in the
// shortfall, where hostset's own default of 15s would have waited it out.
func TestHostReadTakesTheTimeout(t *testing.T) {
	id := goldenIdentity(t)
	base, addr := lookupHost(t, func(w http.ResponseWriter, r *http.Request) {
		// Reading the question lets the server see the reader hang up, which
		// ends the wait below.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	code, _, stderr := readerRun(t, "-host", base, "-header-url", "http://127.0.0.1:1", "-quorum", "1", "-timeout", "300ms", id)
	ue := &url.Error{Op: "Post", URL: base + "/lookup", Err: context.DeadlineExceeded}
	want := fmt.Sprintf("bfinger: lookup at %[1]s: hostset: %[1]s: 0 of 1 host(s) answered, need 1: 127.0.0.1@%[2]s: %[3]s\n", base, addr, ue)
	if stderr != want || code != 2 {
		t.Fatalf("exit %d, stderr\n%q\nwant exit 2 and\n%q", code, stderr, want)
	}
}

// goldenTokenAnswer is an output-list holding the golden token, unmined and
// carrying its funding parent, so verifying it asks the header source for
// the funding tree's root and nothing earlier refuses it.
func goldenTokenAnswer(t *testing.T) (string, uint32) {
	t.Helper()
	g := goldentest.Load(t)
	tok := goldentest.Tx(t, g.Token1TxHex)
	tok.Inputs[0].SourceTransaction = g.Funding(t)
	beef, err := tok.AtomicBEEF(false)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"type":    "output-list",
		"outputs": []map[string]any{{"beef": beef, "outputIndex": 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body), g.FundingHeight
}

// TestHeaderRootTextsAreFrozen pins the header source's refusals of a root
// question, which reach the user through verification as a proof that
// could not be checked.
func TestHeaderRootTextsAreFrozen(t *testing.T) {
	id := goldenIdentity(t)
	tokens, height := goldenTokenAnswer(t)
	host, _ := lookupHost(t, answer(200, tokens))
	path := fmt.Sprintf("/v1/root/%d", height)
	over := `{"height":1,"merkleRoot":"` + strings.Repeat("a", 1<<20) + `"}`

	for _, c := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"status", 503, "", fmt.Sprintf("header service: root for height %d: status 503", height)},
		// Only a 404 means the height is not held yet; another 4xx is an
		// error, however close it sits to one.
		{"a 4xx that is not 404", 410, "", fmt.Sprintf("header service: root for height %d: status 410", height)},
		{"no merkleRoot", 200, fmt.Sprintf(`{"height":%d}`, height), fmt.Sprintf("header service: no merkleRoot for height %d", height)},
		{"not JSON", 200, "nope", "header service answered 200 with a body that is not JSON: " + jsonErr(t, "nope")},
		{"over the bound", 200, over, "headers: response body exceeds the bound (1048576 bytes)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var asked string
			hdr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked = r.URL.Path
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			t.Cleanup(hdr.Close)
			code, _, stderr := readerRun(t, "-host", host, "-header-url", hdr.URL, "-quorum", "1", id)
			want := "bfinger: could not verify: token proof could not be checked: " + c.want + "\n"
			if stderr != want || code != 2 {
				t.Fatalf("exit %d, stderr\n%q\nwant exit 2 and\n%q", code, stderr, want)
			}
			if asked != path {
				t.Fatalf("header source was asked %q, want %q", asked, path)
			}
		})
	}
}

// TestDoctorHeaderTextsAreFrozen pins the header source's refusals of a tip
// question, which doctor prints on its headers line.
func TestDoctorHeaderTextsAreFrozen(t *testing.T) {
	over := `{"height":1,"hash":"` + strings.Repeat("a", 1<<20) + `"}`
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"status", 503, "", "header service: tip: status 503"},
		{"a 2xx that is not 200", 204, "", "header service: tip: status 204"},
		{"not JSON", 200, "nope", "header service answered 200 with a body that is not JSON: " + jsonErr(t, "nope")},
		{"over the bound", 200, over, "headers: response body exceeds the bound (1048576 bytes)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			hdr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/tip" {
					t.Errorf("doctor asked %s", r.URL.Path)
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			t.Cleanup(hdr.Close)
			_, stdout, _ := readerRun(t, "-header-url", hdr.URL, "doctor")
			want := "headers     " + hdr.URL + ": " + c.want
			var got string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "headers ") {
					got = line
				}
			}
			if got != want {
				t.Fatalf("doctor's headers line\n%q\nwant\n%q\nstdout:\n%s", got, want, stdout)
			}
		})
	}
}

// TestAddressRefusalTextsAreFrozen pins every refusal of an address's
// grammar, which bfinger prints as a usage error before anything is dialed.
// An address can break several rules, and the check that runs first decides
// which one the user is told about, so the second group breaks two rules per
// row: one row for every pair of checks that can both fail, which pins the
// order of the whole chain and not only each text.
func TestAddressRefusalTextsAreFrozen(t *testing.T) {
	h65, t33 := strings.Repeat("a", 65), strings.Repeat("t", 33)
	l63, l64 := strings.Repeat("l", 63), strings.Repeat("l", 64)
	d254 := strings.Repeat("a.", 126) + "ab"
	b65 := strings.Repeat("!", 65)
	d255 := strings.Repeat("a.", 126) + ".ab"
	for _, c := range []struct{ addr, want string }{
		{"alice", `resolve: "alice": no @domain; the ecosystem must be named in full as handle@domain.tld`},
		{"alice@", `resolve: "alice@": no @domain; the ecosystem must be named in full as handle@domain.tld`},
		{"alice@@example.com", `resolve: "alice@@example.com": more than one @ separates handle and domain`},
		{"+work@example.com", `resolve: "+work@example.com": handle is empty`},
		{h65 + "@example.com", `resolve: "` + h65 + `@example.com": handle is 65 characters, the limit is 64`},
		{".alice@example.com", `resolve: ".alice@example.com": handle ".alice" must begin and end with a letter or digit`},
		{"al!ce@example.com", `resolve: "al!ce@example.com": handle "al!ce" contains "!"; only a-z, 0-9 and internal . _ - are allowed`},
		{"alice+@example.com", `resolve: "alice+@example.com": tag is empty`},
		{"alice+" + t33 + "@example.com", `resolve: "alice+` + t33 + `@example.com": tag is 33 characters, the limit is 32`},
		{"alice+w!rk@example.com", `resolve: "alice+w!rk@example.com": tag "w!rk" contains "!"; only a-z, 0-9 and internal . _ - are allowed`},
		{"Alice@LKUP", `resolve: "Alice@LKUP": ecosystem "lkup" has no dot, so it is an alias (BRC-169 section 2.1 rule 5); aliases are not supported, name the domain in full`},
		{"alice@" + d254, `resolve: "alice@` + d254 + `": domain is 254 characters, the limit is 253`},
		{"alice@example..com", `resolve: "alice@example..com": domain "example..com" has an empty label`},
		{"alice@" + l64 + ".com", `resolve: "alice@` + l64 + `.com": domain "` + l64 + `.com": label "` + l64 + `" exceeds 63 characters`},
		{"alice@-bad.example.com", `resolve: "alice@-bad.example.com": domain "-bad.example.com": label "-bad" must not begin or end with a hyphen`},
		{"alice@example.com:8443", `resolve: "alice@example.com:8443": domain "example.com:8443" contains ":"; only a-z, 0-9, - and . are allowed`},

		// The order: no @domain, one @, handle, tag, dotless ecosystem,
		// domain. "alice@@example.com" above already pins one @ before the
		// domain; no @domain cannot fail together with the last three.
		{"al!ce", `resolve: "al!ce": no @domain; the ecosystem must be named in full as handle@domain.tld`},
		{"alice+w!rk", `resolve: "alice+w!rk": no @domain; the ecosystem must be named in full as handle@domain.tld`},
		{"al!ce@@example.com", `resolve: "al!ce@@example.com": more than one @ separates handle and domain`},
		{"alice+w!rk@@example.com", `resolve: "alice+w!rk@@example.com": more than one @ separates handle and domain`},
		{"alice@@lkup", `resolve: "alice@@lkup": more than one @ separates handle and domain`},
		{"al!ce+w!rk@example.com", `resolve: "al!ce+w!rk@example.com": handle "al!ce" contains "!"; only a-z, 0-9 and internal . _ - are allowed`},
		{"al!ce@lkup", `resolve: "al!ce@lkup": handle "al!ce" contains "!"; only a-z, 0-9 and internal . _ - are allowed`},
		{"al!ce@-bad.example.com", `resolve: "al!ce@-bad.example.com": handle "al!ce" contains "!"; only a-z, 0-9 and internal . _ - are allowed`},
		{"alice+w!rk@lkup", `resolve: "alice+w!rk@lkup": tag "w!rk" contains "!"; only a-z, 0-9 and internal . _ - are allowed`},
		{"alice+w!rk@-bad.example.com", `resolve: "alice+w!rk@-bad.example.com": tag "w!rk" contains "!"; only a-z, 0-9 and internal . _ - are allowed`},
		{"alice@lk!up", `resolve: "alice@lk!up": ecosystem "lk!up" has no dot, so it is an alias (BRC-169 section 2.1 rule 5); aliases are not supported, name the domain in full`},
		// Within a handle or tag: the length before the characters.
		{b65 + "@example.com", `resolve: "` + b65 + `@example.com": handle is 65 characters, the limit is 64`},
		// Within a domain: the whole length before any label, then label
		// by label its length, its hyphens, its characters.
		{"alice@" + d255, `resolve: "alice@` + d255 + `": domain is 255 characters, the limit is 253`},
		{"alice@-" + l63 + ".com", `resolve: "alice@-` + l63 + `.com": domain "-` + l63 + `.com": label "-` + l63 + `" exceeds 63 characters`},
		{"alice@" + l63 + "!.com", `resolve: "alice@` + l63 + `!.com": domain "` + l63 + `!.com": label "` + l63 + `!" exceeds 63 characters`},
		{"alice@-b!d.example.com", `resolve: "alice@-b!d.example.com": domain "-b!d.example.com": label "-b!d" must not begin or end with a hyphen`},
		{"alice@b!d.-bad.com", `resolve: "alice@b!d.-bad.com": domain "b!d.-bad.com" contains "!"; only a-z, 0-9, - and . are allowed`},
		// The prefixes come off before any check, "acct:" before "@": an
		// "@acct:" address keeps its "acct:" in the handle and is refused
		// for it. The tag is split at the FIRST "+", so a second "+" is in
		// the tag and is refused there.
		{"@acct:alice@example.com", `resolve: "@acct:alice@example.com": handle "acct:alice" contains ":"; only a-z, 0-9 and internal . _ - are allowed`},
		{"alice+a+b@example.com", `resolve: "alice+a+b@example.com": tag "a+b" contains "+"; only a-z, 0-9 and internal . _ - are allowed`},
	} {
		code, _, stderr := readerRun(t, "-header-url", "http://127.0.0.1:1", c.addr)
		if want := "bfinger: " + c.want + "\n"; stderr != want || code != 2 {
			t.Errorf("%s: exit %d, stderr\n%q\nwant exit 2 and\n%q", c.addr, code, stderr, want)
		}
	}
}

// discovery serves routes over TLS as example.com and returns resolve's own
// client, changed only to trust the test certificate and to dial the test
// server for every name. The redirect rule and the body bound are resolve's.
func discovery(t *testing.T, routes map[string]http.HandlerFunc) *http.Client {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	c := resolve.NewHTTPClient(5 * time.Second)
	tr := c.Transport.(*http.Transport)
	tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	addr := srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	return c
}

// TestDiscoveryClientIsResolvesClientAtTheTimeout covers the branch the
// rows below bypass with a test client: with none set, a name is resolved
// with resolve's own client at -timeout, not at resolve's default, and with
// resolve's TLS floor, which the rows' client replaces to trust the test
// certificate.
func TestDiscoveryClientIsResolvesClientAtTheTimeout(t *testing.T) {
	cfg := config.Defaults()
	cfg.Timeout = 7 * time.Second
	if cfg.Timeout == config.Defaults().Timeout {
		t.Fatal("the test timeout must differ from the default to tell them apart")
	}
	c := (&global{cfg: cfg}).discoveryClient()
	if c.Timeout != cfg.Timeout {
		t.Errorf("client timeout %v, want %v", c.Timeout, cfg.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want resolve's *http.Transport", c.Transport)
	}
	if tr.Proxy != nil {
		t.Error("the discovery client takes a proxy from the environment")
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("the discovery client's TLS config is %+v, want TLS 1.2 at the least", tr.TLSClientConfig)
	}
	if tr.TLSHandshakeTimeout != cfg.Timeout {
		t.Errorf("TLS handshake timeout %v, want %v", tr.TLSHandshakeTimeout, cfg.Timeout)
	}
	if c.CheckRedirect == nil {
		t.Fatal("the discovery client follows any redirect")
	}
	first := httptest.NewRequest(http.MethodGet, "https://example.com/manifest.json", nil)
	next := httptest.NewRequest(http.MethodGet, "https://other.example/manifest.json", nil)
	if err := c.CheckRedirect(next, []*http.Request{first}); !errors.Is(err, resolve.ErrCrossOriginRedirect) {
		t.Errorf("a redirect to another origin gave %v, want resolve's refusal", err)
	}
}

// lookupVia runs the lookup command for alice@example.com, with no -host,
// against a domain served by routes, and returns what bfinger prints after
// "bfinger: " (run prints the error's text, an exitError's included).
func lookupVia(t *testing.T, routes map[string]http.HandlerFunc) string {
	t.Helper()
	return lookupAddrVia(t, "alice@example.com", routes)
}

// lookupAddrVia is lookupVia for a given address.
func lookupAddrVia(t *testing.T, addr string, routes map[string]http.HandlerFunc) string {
	t.Helper()
	home := t.TempDir()
	cfg := config.Defaults()
	cfg.Home, cfg.KnownKeys = home, filepath.Join(home, "known_keys")
	cfg.HeaderURL, cfg.Timeout = "http://127.0.0.1:1", 5*time.Second
	g := &global{cfg: cfg, discovery: discovery(t, routes)}
	stdout, _ := tempFile(t)
	stderr, _ := tempFile(t)
	err := cmdLookup(context.Background(), g, []string{addr}, stdout, stderr)
	if err == nil {
		t.Fatal("the lookup succeeded")
	}
	var ee *exitError
	if errors.As(err, &ee) {
		t.Fatalf("exit %d (%q), want the transport error exit 2 gives", ee.code, ee.msg)
	}
	return err.Error()
}

func says(body string) http.HandlerFunc { return answer(200, body) }

const handlesManifest = `{"metanet":{"handles":{"version":"1.0"}}}`

// "acct:" comes off before "@", so "acct:@alice@example.com" is alice at
// example.com: the address is accepted and resolved, and the run ends at
// bfinger's own refusal, well past every address check.
func TestAcctThenAtPrefixIsAccepted(t *testing.T) {
	id := goldenIdentity(t)
	got := lookupAddrVia(t, "acct:@alice@example.com", map[string]http.HandlerFunc{
		"/manifest.json":                       says(handlesManifest),
		"/.well-known/metanet-handles/resolve": says(fmt.Sprintf(`{"metanetHandles":"1.0","handle":"alice","domain":"example.com","identityKey":%q,"certificate":{},"messagebox":"","ttl":60,"revoked":false}`, id)),
	})
	if want := "example.com does not name ls_finger in its manifest and no -host is configured"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestResolveRefusalTextsAreFrozen pins the manifest and handle refusals as
// the lookup command prints them. A row that breaks two rules at once pins
// which check runs first, since the order is what decides the text a user
// sees. The redirect rows also count the requests the bound lets through.
func TestResolveRefusalTextsAreFrozen(t *testing.T) {
	id := goldenIdentity(t)
	resolved := func(handle, domain, key string, revoked bool) string {
		return fmt.Sprintf(`{"metanetHandles":"1.0","handle":%q,"domain":%q,"identityKey":%q,"certificate":{},"messagebox":"","ttl":60,"revoked":%v}`, handle, domain, key, revoked)
	}
	const wellKnown = "/.well-known/metanet-handles/resolve"
	const man = "manifest for example.com: resolve: manifest for example.com: "
	const res = "resolve alice@example.com: resolve: alice@example.com: "
	big := `{"metanet":{"overlays":{"x":"` + strings.Repeat("a", 256<<10) + `"}}}`
	// atBound is a valid manifest with no handles of exactly the bound.
	const head, tail = `{"metanet":{"overlays":{"x":"`, `"}}}`
	atBound := head + strings.Repeat("a", 256<<10-len(head)-len(tail)) + tail
	short, notHex, uncompressed := id[:64], "zz"+id[2:], "04"+id[2:]
	_, hexErr := hex.DecodeString(notHex)
	const badResolve = "https://example.com/%zz"
	_, parseErr := url.Parse(badResolve)
	const self = "https://example.com" + wellKnown + "?handle=alice"
	// padded is an error page over the bound: resolve reads the body before
	// it looks at the status, the opposite order to the header source's.
	padded := strings.Repeat("x", 300<<10)
	var manHops, resHops, leaveHops atomic.Int32
	// Today's bound of five follows five requests and refuses the sixth.
	hops := map[string]*atomic.Int32{
		"manifest redirects more than five times":                   &manHops,
		"answer redirects more than five times":                     &resHops,
		"manifest redirects five times within the origin, then out": &leaveHops,
	}

	for _, c := range []struct {
		name   string
		routes map[string]http.HandlerFunc
		want   string
	}{
		{"manifest status", map[string]http.HandlerFunc{}, man + "status 404"},
		// Only a 200 is a manifest: another 2xx is refused with its status.
		{"manifest status 204", map[string]http.HandlerFunc{"/manifest.json": answer(204, "")}, man + "status 204"},
		{"manifest not JSON", map[string]http.HandlerFunc{"/manifest.json": says("<html>")},
			man + "manifest is not the expected JSON: " + jsonErr(t, "<html>")},
		{"manifest over the bound", map[string]http.HandlerFunc{"/manifest.json": says(big)},
			man + "response body exceeds the bound (262144 bytes)"},
		{"manifest over the bound under a 404", map[string]http.HandlerFunc{"/manifest.json": answer(404, padded)},
			man + "response body exceeds the bound (262144 bytes)"},
		// Exactly at the bound the manifest is read and parsed, and the run
		// goes on to the next refusal.
		{"manifest at the bound", map[string]http.HandlerFunc{"/manifest.json": says(atBound)},
			res + "domain does not offer handle resolution (no metanet.handles in its manifest)"},
		// Host names compare without case, so a redirect to the same host in
		// capitals stays within the origin and is followed: the manifest it
		// lands on is read, and the run ends at bfinger's own refusal.
		{"manifest redirect within the origin in capitals", map[string]http.HandlerFunc{
			"/manifest.json": func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "https://EXAMPLE.COM/m2.json", http.StatusFound)
			},
			"/m2.json": says(handlesManifest),
			wellKnown:  says(resolved("alice", "example.com", id, false)),
		}, "example.com does not name ls_finger in its manifest and no -host is configured"},
		{"manifest redirect to another origin", map[string]http.HandlerFunc{"/manifest.json": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://other.example/manifest.json", http.StatusFound)
		}}, man + (&url.Error{Op: "Get", URL: "https://other.example/manifest.json",
			Err: errors.New("redirect to another origin refused: https://example.com -> https://other.example")}).Error()},
		// The origin is the scheme as well as the host: a downgrade to http
		// on the same host is refused in the same words.
		{"manifest redirect to another scheme", map[string]http.HandlerFunc{"/manifest.json": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://example.com/manifest.json", http.StatusFound)
		}}, man + (&url.Error{Op: "Get", URL: "http://example.com/manifest.json",
			Err: errors.New("redirect to another origin refused: https://example.com -> http://example.com")}).Error()},
		{"manifest redirects more than five times", map[string]http.HandlerFunc{"/manifest.json": counted(&manHops, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://example.com/manifest.json", http.StatusFound)
		})}, man + (&url.Error{Op: "Get", URL: "https://example.com/manifest.json", Err: errors.New("more than 5 redirects")}).Error()},
		// The count is checked before the origin: a sixth hop that leaves
		// the origin is refused for the length of the chain.
		{"manifest redirects five times within the origin, then out", map[string]http.HandlerFunc{"/manifest.json": counted(&leaveHops, func(w http.ResponseWriter, r *http.Request) {
			to := "https://example.com/manifest.json"
			if leaveHops.Load() == 5 {
				to = "https://other.example/manifest.json"
			}
			http.Redirect(w, r, to, http.StatusFound)
		})}, man + (&url.Error{Op: "Get", URL: "https://other.example/manifest.json", Err: errors.New("more than 5 redirects")}).Error()},
		{"no handles", map[string]http.HandlerFunc{"/manifest.json": says(`{"metanet":{"overlays":{"ls_finger":"https://example.com"}}}`)},
			res + "domain does not offer handle resolution (no metanet.handles in its manifest)"},
		{"handles major version", map[string]http.HandlerFunc{"/manifest.json": says(`{"metanet":{"handles":{"version":"2.0"}}}`)},
			res + `metanet.handles.version "2.0": major version is not 1, the only one this build implements`},
		// The major is everything before the first dot, not its first digit.
		{"handles two-digit major version", map[string]http.HandlerFunc{"/manifest.json": says(`{"metanet":{"handles":{"version":"10.0"}}}`)},
			res + `metanet.handles.version "10.0": major version is not 1, the only one this build implements`},
		{"handles with no version", map[string]http.HandlerFunc{"/manifest.json": says(`{"metanet":{"handles":{}}}`)},
			res + `metanet.handles.version "": major version is not 1, the only one this build implements`},
		{"handles major version before the resolve URL", map[string]http.HandlerFunc{"/manifest.json": says(`{"metanet":{"handles":{"version":"2.0","resolve":"http://example.com/handles/resolve"}}}`)},
			res + `metanet.handles.version "2.0": major version is not 1, the only one this build implements`},
		{"http resolve URL", map[string]http.HandlerFunc{"/manifest.json": says(`{"metanet":{"handles":{"version":"1.0","resolve":"http://example.com/handles/resolve"}}}`)},
			res + `not an https URL: metanet.handles.resolve "http://example.com/handles/resolve"`},
		// An https URL with no host is refused in the same words.
		{"resolve URL with no host", map[string]http.HandlerFunc{"/manifest.json": says(`{"metanet":{"handles":{"version":"1.0","resolve":"https:///resolve"}}}`)},
			res + `not an https URL: metanet.handles.resolve "https:///resolve"`},
		{"unparsable resolve URL", map[string]http.HandlerFunc{"/manifest.json": says(`{"metanet":{"handles":{"version":"1.0","resolve":"` + badResolve + `"}}}`)},
			res + `metanet.handles.resolve "` + badResolve + `": ` + parseErr.Error()},
		{"answer over the bound", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest), wellKnown: says(big)},
			res + "response body exceeds the bound (262144 bytes)"},
		{"answer over the bound under a 404", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest), wellKnown: answer(404, padded)},
			res + "response body exceeds the bound (262144 bytes)"},
		{"answer over the bound under a 410", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest), wellKnown: answer(410, padded)},
			res + "response body exceeds the bound (262144 bytes)"},
		{"answer redirect to another origin", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "https://other.example"+wellKnown+"?handle=alice", http.StatusFound)
			}}, res + (&url.Error{Op: "Get", URL: "https://other.example" + wellKnown + "?handle=alice",
			Err: errors.New("redirect to another origin refused: https://example.com -> https://other.example")}).Error()},
		{"answer redirect to another scheme", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "http://example.com"+wellKnown+"?handle=alice", http.StatusFound)
			}}, res + (&url.Error{Op: "Get", URL: "http://example.com" + wellKnown + "?handle=alice",
			Err: errors.New("redirect to another origin refused: https://example.com -> http://example.com")}).Error()},
		{"answer redirects more than five times", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: counted(&resHops, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, self, http.StatusFound)
			})}, res + (&url.Error{Op: "Get", URL: self, Err: errors.New("more than 5 redirects")}).Error()},
		{"not registered, with a code", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: answer(404, `{"metanetHandles":"1.0","error":{"code":"handle-not-found","message":"no such handle"}}`)},
			res + "status 404 (handle-not-found): handle is not registered at this domain"},
		{"not registered, no body", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest), wellKnown: answer(404, "")},
			res + "status 404: handle is not registered at this domain"},
		{"revoked with forwarding", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: answer(410, `{"metanetHandles":"1.0","error":{"code":"handle-revoked"},"forwarding":{"toHandle":"@alice@other.example"}}`)},
			res + "handle revoked (status 410), a forwarding record was served"},
		// A forwarding field that is present counts, even as null.
		{"revoked with a null forwarding", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: answer(410, `{"forwarding":null}`)},
			res + "handle revoked (status 410), a forwarding record was served"},
		{"revoked", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest), wellKnown: answer(410, "")},
			res + "handle revoked (status 410)"},
		{"revoked under a 200", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: says(resolved("alice", "example.com", id, true))},
			res + "handle revoked (status 200)"},
		{"other status, with a code", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: answer(503, `{"error":{"code":"unavailable","message":"later"}}`)},
			res + "status 503 (unavailable)"},
		// The code names the refusal and the message never does.
		{"other status, a message and no code", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: answer(503, `{"error":{"message":"later"}}`)},
			res + "status 503"},
		{"other status, no body", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest), wellKnown: answer(500, "")},
			res + "status 500"},
		{"answer not JSON", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest), wellKnown: says("<html>")},
			res + "answer is not the expected JSON: " + jsonErr(t, "<html>")},
		{"answer major version", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: says(strings.Replace(resolved("alice", "example.com", id, false), `"1.0"`, `"2.1"`, 1))},
			res + `metanetHandles "2.1": major version is not 1, the only one this build implements`},
		{"answer with no metanetHandles", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: says(strings.Replace(resolved("alice", "example.com", id, false), `"metanetHandles":"1.0",`, "", 1))},
			res + `metanetHandles "": major version is not 1, the only one this build implements`},
		{"echo of another handle", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: says(resolved("bob", "example.com", id, false))},
			res + `endpoint echoed "bob" at "example.com", not the handle asked for; refusing an answer that was not for this handle`},
		{"identity key length", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: says(resolved("alice", "example.com", short, false))},
			res + `identityKey "` + short + `" is 64 characters, want 66 (33-byte compressed key, hex)`},
		{"identity key not hex", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: says(resolved("alice", "example.com", notHex, false))},
			res + `identityKey "` + notHex + `" is not hex: ` + hexErr.Error()},
		{"identity key not compressed", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: says(resolved("alice", "example.com", uncompressed, false))},
			res + `identityKey "` + uncompressed + `" is not a compressed key (prefix 04, want 02 or 03)`},
		{"identity key length before hex", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: says(resolved("alice", "example.com", notHex[:64], false))},
			res + `identityKey "` + notHex[:64] + `" is 64 characters, want 66 (33-byte compressed key, hex)`},
		{"answer major version before the echo", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: says(strings.Replace(resolved("bob", "example.com", id, false), `"1.0"`, `"2.0"`, 1))},
			res + `metanetHandles "2.0": major version is not 1, the only one this build implements`},
		{"echo before revoked", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: says(resolved("bob", "example.com", id, true))},
			res + `endpoint echoed "bob" at "example.com", not the handle asked for; refusing an answer that was not for this handle`},
		{"revoked before the identity key", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: says(resolved("alice", "example.com", short, true))},
			res + "handle revoked (status 200)"},
		// Resolution succeeds; the manifest names no lookup service for
		// bfinger to ask, and bfinger's own refusal names the service.
		{"no ls_finger", map[string]http.HandlerFunc{"/manifest.json": says(handlesManifest),
			wellKnown: says(resolved("alice", "example.com", id, false))},
			"example.com does not name ls_finger in its manifest and no -host is configured"},
		// An empty entry names no service: it is refused as an absent one,
		// never dialed as an empty base.
		{"empty ls_finger", map[string]http.HandlerFunc{"/manifest.json": says(`{"metanet":{"overlays":{"ls_finger":""},"handles":{"version":"1.0"}}}`),
			wellKnown: says(resolved("alice", "example.com", id, false))},
			"example.com does not name ls_finger in its manifest and no -host is configured"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := lookupVia(t, c.routes); got != c.want {
				t.Fatalf("printed\n%q\nwant\n%q", got, c.want)
			}
			if n, ok := hops[c.name]; ok && n.Load() != 5 {
				t.Fatalf("the redirecting path was asked %d time(s), want 5", n.Load())
			}
		})
	}
}

// TestPayResolvesThroughTheDiscoveryClient drives pay's name resolution, which
// wraps resolve's refusals in the same words as the lookup command. The test
// domain is reachable only through g's discovery client, so the success row
// also pins that pay resolves with g.discoveryClient() and no other.
func TestPayResolvesThroughTheDiscoveryClient(t *testing.T) {
	id := goldenIdentity(t)
	const wellKnown = "/.well-known/metanet-handles/resolve"
	alice := `{"metanetHandles":"1.0","handle":"alice","domain":"example.com","identityKey":"` + id + `","certificate":{},"messagebox":"","ttl":60,"revoked":false}`
	cfg := config.Defaults()
	via := func(routes map[string]http.HandlerFunc) *global {
		return &global{cfg: cfg, discovery: discovery(t, routes)}
	}

	pub, name, err := resolveRecipient(context.Background(), via(map[string]http.HandlerFunc{
		"/manifest.json": says(handlesManifest), wellKnown: says(alice),
	}), "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(pub.Compressed()); got != id || name != "alice@example.com" {
		t.Fatalf("resolved %s as %q, want %s as %q", got, name, id, "alice@example.com")
	}

	for _, c := range []struct {
		name   string
		routes map[string]http.HandlerFunc
		want   string
	}{
		{"manifest status", map[string]http.HandlerFunc{},
			"manifest for example.com: resolve: manifest for example.com: status 404"},
		{"no handles", map[string]http.HandlerFunc{"/manifest.json": says(`{"metanet":{}}`)},
			"resolve alice@example.com: resolve: alice@example.com: domain does not offer handle resolution (no metanet.handles in its manifest)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := resolveRecipient(context.Background(), via(c.routes), "alice@example.com")
			if err == nil || err.Error() != c.want {
				t.Fatalf("got %v, want\n%q", err, c.want)
			}
		})
	}
}
