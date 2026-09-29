package verify_test

import (
	"testing"

	"github.com/bsv-blockchain/go-sdk/transaction/chaintracker"
)

// The store-carrier check refuses on kind and identity BEFORE it asks the
// header source. Every other store row's tracker proves the root, so a
// refactor that moved the proof ahead of those checks would pass them all
// while turning these answers into REFUSED-BUMP (no root known) or ERROR (no
// header source). Each row below pairs a carrier refused on its own content
// with a header source that would refuse or fail, and a tracker that is
// down must not have been asked at all: a carrier refused anyway costs the
// reader no header lookup. The last two rows are the controls: the same
// header sources DO answer REFUSED-BUMP and ERROR, and the down tracker IS
// asked, once the carrier itself passes.
func TestStoreCarrierRefusesBeforeTheProof(t *testing.T) {
	f := newFixture(t)
	type row struct {
		name   string
		build  func(kind uint8) storeScene
		down   bool
		code   string
		reason string
		// asked is whether the header source must have been called.
		asked bool
	}
	wrongKind := func(uint8) storeScene { return f.served(f.memberTx(f.w1, f.id1, 1, 0xb0, 0)) }
	// Minted by w2 for id2, so it is valid in its own right, and asked
	// under id1.
	foreign := func(kind uint8) storeScene { return f.served(f.memberTx(f.w2, f.id2, kind, 0xb1, 0)) }
	valid := func(kind uint8) storeScene { return f.served(f.memberTx(f.w1, f.id1, kind, 0xb2, 0)) }
	rows := []row{
		{name: "wrong kind, no root known", build: wrongKind,
			code: "REFUSED-DECODE", reason: "{what}: the carrier holds a kind 1 record, not kind {kind}"},
		{name: "wrong kind, header source down", build: wrongKind, down: true,
			code: "REFUSED-DECODE", reason: "{what}: the carrier holds a kind 1 record, not kind {kind}"},
		{name: "foreign identity, no root known", build: foreign,
			code: "REFUSED-KEY", reason: "{what}: the record names a different identity"},
		{name: "foreign identity, header source down", build: foreign, down: true,
			code: "REFUSED-KEY", reason: "{what}: the record names a different identity"},
		{name: "control: valid, no root known", build: valid,
			code: "REFUSED-BUMP", reason: "{what}: the funding parent is not proven in the header source"},
		{name: "control: valid, header source down", build: valid, down: true,
			code: "ERROR", reason: "{what}: could not verify: header source unavailable", asked: true},
	}
	for _, e := range storeEntries() {
		for _, rw := range rows {
			t.Run(e.name+"/"+rw.name, func(t *testing.T) {
				s := rw.build(e.kind)
				s.id = f.id1
				dt := &downTracker{}
				var tr chaintracker.ChainTracker = emptyTracker()
				if rw.down {
					tr = dt
				}
				s.tracker = tr
				runStoreCase(t, f, e, storeCase{code: rw.code, reason: rw.reason, undecided: rw.code == "ERROR"}, s)
				if rw.down && (dt.calls > 0) != rw.asked {
					t.Errorf("header source asked %d time(s), want asked=%v", dt.calls, rw.asked)
				}
			})
		}
	}
}
