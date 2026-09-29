package main

import (
	"flag"
	"reflect"
	"strings"
	"testing"
)

func TestHoistReaderFlags(t *testing.T) {
	global := flag.NewFlagSet("bfinger", flag.ContinueOnError)
	global.String("header-url", "", "")
	global.String("config", "", "")
	global.Bool("json", false, "")
	global.Bool("v", false, "")
	for _, tc := range []struct{ in, want string }{
		// A reader flag before the address moves after it.
		{"-ansi a@b.c", "a@b.c -ansi"},
		{"-field status -l a@b.c", "a@b.c -field status -l"},
		{"-field=status a@b.c", "a@b.c -field=status"},
		// Global flags and their values stay put, in order.
		{"-header-url woc:main -ansi a@b.c -yes", "-header-url woc:main a@b.c -ansi -yes"},
		{"-json -ansi a@b.c", "-json a@b.c -ansi"},
		{"--ansi -interval 5s a@b.c", "a@b.c --ansi -interval 5s"},
		// verify reads the same flags after its own word.
		{"-yes verify a@b.c", "verify -yes a@b.c"},
		// Nothing to move, an unknown flag, an owner command, and "--" are left alone.
		{"a@b.c -ansi", "a@b.c -ansi"},
		{"-nosuch a@b.c", "-nosuch a@b.c"},
		{"-ansi publish", "-ansi publish"},
		{"-ansi -- a@b.c", "-ansi -- a@b.c"},
		{"-ansi", "-ansi"},
	} {
		got := hoistReaderFlags(strings.Fields(tc.in), global, isCommand)
		if !reflect.DeepEqual(got, strings.Fields(tc.want)) {
			t.Errorf("%q: got %q, want %q", tc.in, strings.Join(got, " "), tc.want)
		}
	}
}

func TestRotationSeqNeverUnderstatesOrWraps(t *testing.T) {
	mined := &lookupResult{Seq: 16, Mined: true}
	if got := rotationSeq(mined); got != 16 {
		t.Errorf("mined: %d", got)
	}
	unmined := &lookupResult{Seq: 16, pinOK: true}
	unmined.pin.Seq = 15
	if got := rotationSeq(unmined); got != 16 {
		t.Errorf("unmined over a pin at 15: %d", got)
	}
	trusted := &lookupResult{Seq: 3, pinOK: true} // a keys trust pin is at 0
	if got := rotationSeq(trusted); got != 1 {
		t.Errorf("unmined over a pin at 0: %d", got)
	}
}
