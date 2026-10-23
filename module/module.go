// Package module is the finger overlay module in Go: tm_finger and
// ls_finger, written to bcommon's engine-neutral contract
// (github.com/lightwebinc/bcommon/module) so that any Go overlay host that
// carries the adapter mounts it. It is the twin of the TypeScript module
// under host/src, built from the Go core: the record is the record
// package's codec and rules, the token and funding tags and the key
// derivations are the token package's, the canonical encodings bcommon's.
//
// The module takes no configuration: it manages the one topic tm_finger
// and serves the one lookup service ls_finger, and a host that mounts it
// must carry tm_finger. It prices no question, so there is no terms route.
package module

import (
	"errors"
	"strings"

	bmodule "github.com/lightwebinc/bcommon/module"
)

// Name is the module's name, as a host's module list names it.
const Name = "finger"

// Topic is the topic the module manages.
const Topic = "tm_finger"

// LookupName is the lookup service the module serves.
const LookupName = "ls_finger"

// Build is the version the host logs; a binary sets it.
var Build = "unversioned"

// Mounted is the module: the contract's Module, and its two halves.
type Mounted struct {
	Module bmodule.Module
	TM     *TopicManager
	LS     *Service
}

// Mount builds the module for a host. overlayTopics is OVERLAY_TOPICS, when
// known: a host that does not carry tm_finger has nothing for the module to
// manage, and is told so rather than mounting a manager for a topic it
// refuses.
func Mount(host bmodule.Host, overlayTopics []string) (*Mounted, error) {
	if host == nil {
		return nil, errors.New("finger: no host")
	}
	if len(overlayTopics) > 0 {
		carried := false
		for _, t := range overlayTopics {
			carried = carried || t == Topic
		}
		if !carried {
			return nil, errors.New("finger: OVERLAY_TOPICS does not name " + Topic + ", the topic this module manages")
		}
	}
	tm := NewTopicManager(host)
	ls := NewService(host)
	m := host.Metrics()
	m.Gauge("finger_pending_tokens", func() float64 { return float64(ls.Counts().Pending) })
	m.Gauge("finger_indexed_tokens", func() float64 { return float64(ls.Counts().Tokens) })
	m.Gauge("finger_indexed_carriers", func() float64 { return float64(ls.Counts().Carriers) })
	m.Gauge("finger_identities", func() float64 { return float64(ls.Counts().Identities) })
	m.Gauge("finger_killed_identities", func() float64 { return float64(ls.Counts().Killed) })
	host.Log("finger module mounted", map[string]any{"topics": Topic, "lookup": LookupName, "build": Build})
	return &Mounted{
		Module: bmodule.Module{
			Topics:  map[string]bmodule.TopicManager{Topic: tm},
			Lookups: map[string]bmodule.LookupService{LookupName: ls},
		},
		TM: tm,
		LS: ls,
	}, nil
}

// New is the factory a host calls, with OVERLAY_TOPICS from getenv.
func New(host bmodule.Host, getenv func(string) string) (*Mounted, error) {
	var topics []string
	if getenv != nil {
		for _, t := range strings.Split(getenv("OVERLAY_TOPICS"), ",") {
			if t = strings.TrimSpace(t); t != "" {
				topics = append(topics, t)
			}
		}
	}
	return Mount(host, topics)
}
