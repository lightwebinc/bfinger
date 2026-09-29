package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/lightwebinc/bcommon/nodeapi"
	"github.com/lightwebinc/bcommon/publish"
	"github.com/lightwebinc/bfinger/internal/config"
)

// The request envelope pinned by value, as bfinger's own two construction
// sites build the client: g.node(), behind every owner command's node calls,
// and the settlement leg plane() builds for settle = rpc:<url>.
//
// The id "bfinger" is bfinger's, not the library's. nodeapi has no default id
// and refuses an empty one, so this literal is on the wire only because both
// sites set it, and a third site that forgot would fail rather than send some
// other name. It is the one application literal this client puts on the
// wire, and nothing else checks it: nodeapi's own tests use a neutral id and
// never see this one, and the client ignores the id in the answer. So a change
// to it, to its type, to the protocol version or to params-as-array would
// reach a node unnoticed. This test reads the raw body a real Call sends
// through each site and compares every member against literals.
//
// Each site talks to its own server, and every request must arrive at the
// server its site was configured with and none at the other. Otherwise a
// settlement leg that reused the node's client would still put "bfinger" on
// the wire and pass, while settle = rpc:<url> no longer reached its url.
func TestRPCRequestEnvelopeIsFrozen(t *testing.T) {
	atNode, atSettle := &capture{}, &capture{}
	nodeURL, settleURL := atNode.serve(t), atSettle.serve(t)

	cfg := config.Defaults()
	cfg.RPC, cfg.Asset, cfg.Facade = nodeURL, "http://127.0.0.1:2", "http://127.0.0.1:3"
	cfg.RPCUser, cfg.RPCPass = "u", "p"
	cfg.Settle = "rpc:" + settleURL
	g := &global{cfg: cfg}
	node, _, err := g.node()
	if err != nil {
		t.Fatal(err)
	}
	l, err := g.plane()
	if err != nil {
		t.Fatal(err)
	}
	settler, ok := l.settle.(*publish.RPCSettler)
	if !ok {
		t.Fatalf("settle = rpc: built %T, want *publish.RPCSettler", l.settle)
	}
	ctx := context.Background()

	type rpcCase struct {
		name   string
		call   func() error
		method string
		params string
	}
	cases := func(rpc *nodeapi.RPC) []rpcCase {
		return []rpcCase{
			{
				name:   "nil params",
				call:   func() error { return rpc.Call(ctx, "getinfo", nil, nil) },
				method: `"getinfo"`,
				params: `[]`,
			},
			{
				name:   "GetInfo",
				call:   func() error { _, err := rpc.GetInfo(ctx); return err },
				method: `"getinfo"`,
				params: `[]`,
			},
			{
				name: "GenerateToAddress",
				call: func() error {
					_, err := rpc.GenerateToAddress(ctx, 3, "mhkGqU8gsK9V6xBf7fGN7AmmJ6R9WfHMdV")
					return err
				},
				method: `"generatetoaddress"`,
				params: `[3,"mhkGqU8gsK9V6xBf7fGN7AmmJ6R9WfHMdV"]`,
			},
			{
				name:   "SendRawTransaction",
				call:   func() error { _, err := rpc.SendRawTransaction(ctx, "0100000000"); return err },
				method: `"sendrawtransaction"`,
				params: `["0100000000"]`,
			},
		}
	}
	sites := []struct {
		name       string
		own, other *capture
		cases      []rpcCase
	}{
		{"node", atNode, atSettle, cases(node)},
		{"settle", atSettle, atNode, append(cases(settler.RPC), rpcCase{
			// The leg itself, as a publish drives it: an empty transaction's
			// standard serialisation is version, two zero counts, locktime.
			name:   "RPCSettler.Submit",
			call:   func() error { return settler.Submit(ctx, transaction.NewTransaction()) },
			method: `"sendrawtransaction"`,
			params: `["01000000000000000000"]`,
		})},
	}
	for _, s := range sites {
		for _, c := range s.cases {
			t.Run(s.name+"/"+c.name, func(t *testing.T) {
				s.own.take()
				s.other.take()
				if err := c.call(); err != nil {
					t.Fatalf("call: %v", err)
				}
				if stray := s.other.take(); len(stray) != 0 {
					t.Fatalf("%d requests reached the other site's server", len(stray))
				}
				bodies := s.own.take()
				if len(bodies) != 1 {
					t.Fatalf("%d requests at this site's server, want 1", len(bodies))
				}
				var members map[string]json.RawMessage
				if err := json.Unmarshal(bodies[0], &members); err != nil {
					t.Fatalf("body %s is not a JSON object: %v", bodies[0], err)
				}
				names := make([]string, 0, len(members))
				for k := range members {
					names = append(names, k)
				}
				sort.Strings(names)
				if got := strings.Join(names, ","); got != "id,jsonrpc,method,params" {
					t.Errorf("members %s, want exactly id,jsonrpc,method,params (body %s)", got, bodies[0])
				}
				want := map[string]string{
					"id":      `"bfinger"`,
					"jsonrpc": `"1.0"`,
					"method":  c.method,
					"params":  c.params,
				}
				for _, k := range []string{"id", "jsonrpc", "method", "params"} {
					if got := string(members[k]); got != want[k] {
						t.Errorf("%s = %s, want %s (body %s)", k, got, want[k], bodies[0])
					}
				}
			})
		}
	}
}

// capture is a node that records every request body and answers null.
type capture struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *capture) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		c.mu.Lock()
		c.bodies = append(c.bodies, b)
		c.mu.Unlock()
		w.Header().Set("content-type", "application/json")
		// The answer's id deliberately differs: the client does not read it.
		_, _ = w.Write([]byte(`{"result":null,"error":null,"id":"not-the-request-id"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// take returns what arrived since the last take and forgets it.
func (c *capture) take() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.bodies
	c.bodies = nil
	return b
}
