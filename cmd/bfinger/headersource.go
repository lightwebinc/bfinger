package main

import (
	"fmt"

	"github.com/lightwebinc/bcommon/headers"
)

// headers is the chain tracker every proof is checked against: the
// configured header source, checked at the configured network's
// proof-of-work floor when it serves header fields (WhatsOnChain,
// chaintracks). A bridge's /v1 source serves roots only and is taken as
// given.
func (g *global) headers() *headers.Client {
	c := headers.New(g.cfg.HeaderURL)
	if c.Kind != headers.Native {
		c.Network = g.cfg.Network
	}
	return c
}

// checkHeaderSource refuses a header source that does not parse, or a
// WhatsOnChain source for a network other than the configured one: woc:test
// read on mainnet would fail every proof at the mainnet floor, and woc:main
// read on another network would check nothing the reader meant. A
// chaintracks service can serve any chain, so it takes the configured
// network. An empty source is left to the commands that need one.
func checkHeaderSource(spec, network string) error {
	if spec == "" {
		return nil
	}
	kind, _, implied, err := headers.Parse(spec)
	if err != nil {
		return err
	}
	if kind == headers.WhatsOnChain && implied != network {
		return fmt.Errorf("header source %s is the %s network but network is %s; set network = %s", spec, implied, network, implied)
	}
	return nil
}
