package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lightwebinc/bcommon/publish"
	"github.com/lightwebinc/bcommon/unicast"
)

// facadeRetries are the waits between tries of one facade when there are
// several, before it is counted as having missed an object.
var facadeRetries = []time.Duration{time.Second, 3 * time.Second}

// facadeList splits a facade setting: one base URL, or several separated by
// commas. An empty entry is refused, since it would be a facade nobody meant.
func facadeList(spec string) ([]string, error) {
	var out []string
	for _, f := range strings.Split(spec, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			return nil, usage(fmt.Sprintf("facade %q has an empty entry", spec))
		}
		out = append(out, f)
	}
	return out, nil
}

// facadeFor is the object leg for a facade setting. One URL is that facade.
// Several are each sent every object, for a publisher with no plane behind
// it who wants more than one host to hold the record: the submit is
// idempotent, so a host that already holds an object answers DUPLICATE. An
// object counts as published once one of them took it; each facade that
// missed it is named on stderr, and `publish -resume -facade URL` seeds it
// later.
func facadeFor(spec string, stderr *os.File) (*publish.Facade, error) {
	list, err := facadeList(spec)
	if err != nil {
		return nil, err
	}
	if len(list) == 1 {
		return &publish.Facade{Base: list[0]}, nil
	}
	set := &unicast.Set{Hosts: list, Need: 1, Retries: facadeRetries, Note: func(format string, a ...any) {
		if stderr != nil {
			fmt.Fprintf(stderr, "%s; publish -resume -facade URL re-sends it\n", strings.TrimRight(fmt.Sprintf(format, a...), "\n"))
		}
	}}
	return set.Facade("object"), nil
}
