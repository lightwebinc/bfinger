package messagebox_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/lightwebinc/bfinger/internal/messagebox"
	"github.com/lightwebinc/bfinger/internal/messagebox/mbtest"
)

func TestSendListAcknowledge(t *testing.T) {
	srv := mbtest.NewServer()
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	alice, bob := "028d37b941208cd6b8a4c28288eda5f2f16c2b3ab0fcb6d13c18b47fe37b971fc1", "02c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5"
	a := &messagebox.Client{Base: hs.URL, Fetch: mbtest.As(hs.URL, alice)}
	b := &messagebox.Client{Base: hs.URL, Fetch: mbtest.As(hs.URL, bob)}
	ctx := context.Background()

	if err := a.Send(ctx, bob, "inbox", "msg-001", "hello"); err != nil {
		t.Fatal(err)
	}
	// The request is the vector's shape: {"message": {recipient, messageBox, messageId, body}}.
	m, _ := srv.Seen[0]["message"].(map[string]any)
	if m["recipient"] != bob || m["messageBox"] != "inbox" || m["messageId"] != "msg-001" || m["body"] != "hello" {
		t.Fatalf("sendMessage body %v", srv.Seen[0])
	}
	msgs, err := b.List(ctx, "inbox")
	if err != nil || len(msgs) != 1 || msgs[0].Sender != alice || msgs[0].Body != "hello" {
		t.Fatalf("%+v %v", msgs, err)
	}
	if err := b.Acknowledge(ctx, "inbox", []string{"msg-001", "nonexistent"}); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := b.List(ctx, "inbox"); len(msgs) != 0 {
		t.Fatalf("acknowledged and still listed: %+v", msgs)
	}
	srv.Price = true
	if err := a.Send(ctx, bob, "inbox", "msg-002", "paid"); !errors.Is(err, messagebox.ErrPayment) {
		t.Fatalf("a priced box: %v", err)
	}
	// Unauthenticated is refused by the server, and reported.
	if err := (&messagebox.Client{Base: hs.URL, Fetch: mbtest.As(hs.URL, "")}).Send(ctx, bob, "inbox", "x", "y"); err == nil {
		t.Fatal("no identity: sent")
	}
}
