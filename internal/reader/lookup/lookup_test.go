package lookup

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/lightwebinc/bcommon/hostset"
	bclookup "github.com/lightwebinc/bcommon/lookup"
)

// fixture reads a request body the host module actually parses, vendored
// verbatim under testdata/fixtures.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fixtures", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// identityKeyHex is the BRC-169 A.4 sample key.
const identityKeyHex = "0359c5f3bfe249f6c0ca99d0e9cc1517da51a511f3d04f18e47a5d7ae55f04008c"

// The host module parses exactly these bytes. A test that only round-trips
// through this package would not notice a renamed member.
func TestFingerQuestionMatchesTheFixtureBytes(t *testing.T) {
	key, err := hex.DecodeString(identityKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(FingerQuestion(key))
	if err != nil {
		t.Fatal(err)
	}
	if want := fixture(t, "lookup_question_finger"); !bytes.Equal(got, want) {
		t.Fatalf("marshalled\n%s\nfixture\n%s", got, want)
	}
}

// The finger question reaches the wire as the fixture's bytes through this
// package's Query, the path bfinger's reader takes, and an empty output-list
// comes back as one answer.
func TestQueryPostsTheFingerQuestionBytes(t *testing.T) {
	var gotBody, gotPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(b)
		gotPath.Store(r.Method + " " + r.URL.Path)
		_, _ = w.Write([]byte(`{"type":"output-list","outputs":[]}`))
	}))
	t.Cleanup(srv.Close)
	key, _ := hex.DecodeString(identityKeyHex)
	hs := &hostset.Client{Source: hostset.Static{Bases: []string{srv.URL}}}
	answers, err := Query(context.Background(), hs, srv.URL, FingerQuestion(key))
	if err != nil {
		t.Fatal(err)
	}
	if gotPath.Load() != "POST /lookup" {
		t.Errorf("request was %v", gotPath.Load())
	}
	if !bytes.Equal(gotBody.Load().([]byte), fixture(t, "lookup_question_finger")) {
		t.Errorf("body on the wire was %s", gotBody.Load())
	}
	if len(answers) != 1 || answers[0].Answer.Type != TypeOutputList || len(answers[0].Answer.Outputs) != 0 {
		t.Fatalf("answers = %+v", answers)
	}
}

// The re-exported sentinel is the library's own value, so a caller matching
// either name matches both.
func TestSentinelIsTheLibrarys(t *testing.T) {
	if ErrNotOutputList != bclookup.ErrNotOutputList || !errors.Is(bclookup.ErrNotOutputList, ErrNotOutputList) {
		t.Fatal("ErrNotOutputList is not the library's sentinel")
	}
}

// The sub-store question's bytes, by literal. The host module parses
// {identityKey, carrier} with the carrier in display order, so a renamed
// member or a commitment sent in hash byte order would be a question that
// matches nothing.
func TestCarrierQuestionBytesAreFrozen(t *testing.T) {
	key, err := hex.DecodeString(identityKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	var c [32]byte
	for i := range c {
		c[i] = byte(i)
	}
	got, err := json.Marshal(CarrierQuestion(key, c))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"service":"ls_finger","query":{"identityKey":"0359c5f3bfe249f6c0ca99d0e9cc1517da51a511f3d04f18e47a5d7ae55f04008c","carrier":"1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100"}}`
	if string(got) != want {
		t.Fatalf("marshalled\n%s\nwant\n%s", got, want)
	}
}
