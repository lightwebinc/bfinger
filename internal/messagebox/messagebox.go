// Package messagebox is a BRC-33 message box client, reduced to the three
// calls a reader and a deliverer need: send a message to one recipient's
// box, list a box, and acknowledge what was taken from it.
//
// Every call is mutually authenticated (BRC-103/104) by go-sdk's AuthFetch
// under the caller's wallet, so the server knows who sent a message and who
// is listing a box, and the recipient learns the sender's identity key from
// the server. A box that asks for payment is refused rather than paid: the
// wallet handed to AuthFetch cannot create an action, so nothing here spends.
package messagebox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	authhttp "github.com/bsv-blockchain/go-sdk/auth/clients/authhttp"
	"github.com/bsv-blockchain/go-sdk/wallet"
)

// Fetcher is the authenticated transport: AuthFetch in use, anything with
// its Fetch in a test.
type Fetcher interface {
	Fetch(ctx context.Context, url string, cfg *authhttp.SimplifiedFetchRequestOptions) (*http.Response, error)
}

// Client talks to one message box server.
type Client struct {
	Base  string
	Fetch Fetcher
}

// ErrPayment is a box that wants paying, which this client never does.
var ErrPayment = errors.New("messagebox: the box asks for payment, which bfinger does not make")

// noPay is the caller's wallet with action creation refused, so AuthFetch's
// answer to a 402 (pay and retry) cannot spend anything.
type noPay struct{ wallet.Interface }

func (noPay) CreateAction(context.Context, wallet.CreateActionArgs, string) (*wallet.CreateActionResult, error) {
	return nil, ErrPayment
}

// New is a client for base that authenticates as w.
func New(base string, w wallet.Interface) *Client {
	f := authhttp.New(noPay{w}, authhttp.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	return &Client{Base: strings.TrimRight(base, "/"), Fetch: f}
}

// Message is one message in a box.
type Message struct {
	MessageID string `json:"messageId"`
	Body      string `json:"body"`
	Sender    string `json:"sender"`
	CreatedAt string `json:"created_at,omitempty"`
}

// maxAnswer bounds what a server's answer may be.
const maxAnswer = 16 << 20

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, err := c.Fetch.Fetch(ctx, c.Base+path, &authhttp.SimplifiedFetchRequestOptions{
		Method: http.MethodPost, Headers: map[string]string{"Content-Type": "application/json"}, Body: body,
	})
	if err != nil {
		if errors.Is(err, ErrPayment) {
			return ErrPayment
		}
		return fmt.Errorf("messagebox %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return fmt.Errorf("messagebox %s: %w", path, err)
	}
	if len(raw) > maxAnswer {
		return fmt.Errorf("messagebox %s: the answer exceeds %d bytes", path, maxAnswer)
	}
	var st struct {
		Status      string `json:"status"`
		Code        string `json:"code"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(raw, &st)
	if resp.StatusCode == http.StatusPaymentRequired {
		return ErrPayment
	}
	if resp.StatusCode != http.StatusOK || st.Status != "success" {
		return fmt.Errorf("messagebox %s: status %d %s %s", path, resp.StatusCode, st.Code, st.Description)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("messagebox %s: %w", path, err)
		}
	}
	return nil
}

// Send puts body in recipient's box under messageID.
func (c *Client) Send(ctx context.Context, recipient, box, messageID, body string) error {
	return c.post(ctx, "/sendMessage", map[string]any{"message": map[string]any{
		"recipient": recipient, "messageBox": box, "messageId": messageID, "body": body,
	}}, nil)
}

// List is every message in the caller's box.
func (c *Client) List(ctx context.Context, box string) ([]Message, error) {
	var out struct {
		Messages []Message `json:"messages"`
	}
	if err := c.post(ctx, "/listMessages", map[string]any{"messageBox": box}, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// Acknowledge removes messages from the caller's box. Acknowledging a
// message the server no longer holds succeeds.
func (c *Client) Acknowledge(ctx context.Context, box string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return c.post(ctx, "/acknowledgeMessage", map[string]any{"messageBox": box, "messageIds": ids}, nil)
}
