package module

import (
	"context"
	"slices"
	"strings"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/transaction"

	bmodule "github.com/lightwebinc/bcommon/module"
)

// RefuseReasons are the refusal vocabulary, the finger_refused_total reason
// label. Fixed and small because it is a label; the detail goes to the log.
var RefuseReasons = []string{"not-pushdrop", "bad-tag", "bad-sig", "bad-record", "bad-lock", "mineable", "non-canonical-unlocking", "other"}

// AdmitKinds are what an accepted submission was. "spend" is a transaction
// that admits nothing but consumes outputs the topic holds (the sweep of a
// funding tree): accepted for its inputs alone, and counted so that a
// sweep is visible on the same series as the objects it retracts.
var AdmitKinds = []string{"token", "carrier", "funding", "spend"}

// TopicManager is tm_finger: it admits each object on its own validity
// (spec section 7): a state token whose field signature verifies, the one
// record output of a carrier whose record decodes, whose lock is the
// identity's record key, whose signature verifies and which cannot be
// mined, and every funding output, so that a later spend of a funding
// output is one the engine sees and reports.
//
// It cannot check the token's lock derivation, because the identity key is
// not in the token; that check, and every rule that needs the previous
// state, is the lookup service's. Its verdict never depends on what the
// host holds, so a refusal is returned, and recorded, rather than raised:
// the same object refused again is refused again.
type TopicManager struct {
	host bmodule.Host
}

var (
	_ bmodule.TopicManager    = (*TopicManager)(nil)
	_ bmodule.InputIdentifier = (*TopicManager)(nil)
)

// NewTopicManager makes tm_finger, presetting every admission kind and
// refusal reason at zero: a lane refusing everything looks busier than a
// healthy one, and an alert needs the zero to compare against.
func NewTopicManager(host bmodule.Host) *TopicManager {
	m := host.Metrics()
	for _, kind := range AdmitKinds {
		m.Preset("finger_admitted_total", map[string]string{"kind": kind})
	}
	for _, reason := range RefuseReasons {
		m.Preset("finger_refused_total", map[string]string{"reason": reason})
	}
	return &TopicManager{host: host}
}

// IdentifyAdmissibleOutputs implements bcommon's module.TopicManager. The
// rules read the submission as it arrived (bcommon module.WithSubmission),
// as the TypeScript manager reads the submitted BEEF; a host that does not
// hand the bytes over is answered from the parsed BEEF's transaction.
func (t *TopicManager) IdentifyAdmissibleOutputs(ctx context.Context, beef *transaction.Beef, txid *chainhash.Hash, previousCoins []uint32) (overlay.AdmittanceInstructions, error) {
	if raw, ok := bmodule.Submission(ctx); ok {
		return t.Admit(raw, previousCoins), nil
	}
	var tx *transaction.Transaction
	if beef != nil && txid != nil {
		tx = beef.FindTransactionByHash(txid)
	}
	return t.admitTx(tx, previousCoins), nil
}

// Admit decides one submission's BEEF with the previous coins (by input
// index), as the TypeScript manager's identifyAdmissibleOutputs does. It
// never fails: whatever arrived from an open plane is the sender's problem,
// not a host fault.
func (t *TopicManager) Admit(beef []byte, previousCoins []uint32) overlay.AdmittanceInstructions {
	_, tx, _, err := transaction.ParseBeef(beef)
	if err != nil {
		tx = nil
	}
	return t.admitTx(tx, previousCoins)
}

func (t *TopicManager) admitTx(tx *transaction.Transaction, previousCoins []uint32) overlay.AdmittanceInstructions {
	// Every previous coin is RETAINED in every case, even when the spending
	// object is refused: it is spent on chain regardless, and keeping it is
	// what lets a formula's history walk the chain of tokens, and what keeps
	// a carrier's funding output in storage, marked spent by the carrier,
	// so that a second spend of it (the kill switch) is one the engine
	// reports. Dropping it would delete a fact because a later object was
	// bad.
	retain := append([]uint32{}, previousCoins...)
	refuse := func(reason, txid string) overlay.AdmittanceInstructions {
		t.host.Metrics().Inc("finger_refused_total", map[string]string{"reason": reason}, 1)
		t.host.Log("tm_finger refused", map[string]any{"txid": txid, "reason": reason})
		return overlay.AdmittanceInstructions{OutputsToAdmit: []uint32{}, CoinsToRetain: retain}
	}
	if tx == nil {
		return refuse(refuseOther, "?")
	}
	txid := tx.TxID().String()

	admitted := map[uint32]bool{}
	tokens, funding := 0, 0
	tokenReason := ""
	for i, out := range tx.Outputs {
		if out == nil || out.LockingScript == nil {
			continue
		}
		shape, tok := inspectToken(out.LockingScript)
		if shape == isToken {
			if tok.valid {
				admitted[uint32(i)] = true //nolint:gosec // an output index
				tokens++
			} else if tokenReason == "" {
				tokenReason = refuseBadSig
			}
			continue
		}
		if shape == badTag {
			if tokenReason == "" {
				tokenReason = "bad-tag"
			}
			continue
		}
		// A funding output has one field and a token three, so the shapes
		// never collide; a token's change output is a funding output too.
		if isFunding(out.LockingScript) {
			admitted[uint32(i)] = true //nolint:gosec // an output index
			funding++
		}
	}

	carriers := 0
	c, carrierReason := decodeCarrier(tx)
	if carrierReason == "" {
		admitted[c.outputIndex] = true
		carriers = 1
	}

	if len(admitted) > 0 {
		m := t.host.Metrics()
		if tokens > 0 {
			m.Inc("finger_admitted_total", map[string]string{"kind": "token"}, float64(tokens))
		}
		if carriers > 0 {
			m.Inc("finger_admitted_total", map[string]string{"kind": "carrier"}, float64(carriers))
		}
		if funding > 0 {
			m.Inc("finger_admitted_total", map[string]string{"kind": "funding"}, float64(funding))
		}
		out := make([]uint32, 0, len(admitted))
		for i := range admitted {
			out = append(out, i)
		}
		slices.Sort(out)
		return overlay.AdmittanceInstructions{OutputsToAdmit: out, CoinsToRetain: retain}
	}

	// Nothing finger-shaped among the outputs, but the transaction consumes
	// outputs the topic holds: the sweep of a funding tree, or any spend of
	// a funding output to somewhere else. It is accepted for its inputs
	// alone: the engine marks each previous output spent and reports it to
	// the lookup service, and a retained coin is kept as consumed rather
	// than deleted.
	if tokenReason == "" && carrierReason == refuseNotPushDrop && len(previousCoins) > 0 {
		t.host.Metrics().Inc("finger_admitted_total", map[string]string{"kind": "spend"}, 1)
		t.host.Log("tm_finger accepted a spend of held outputs", map[string]any{"txid": txid, "inputs": len(previousCoins)})
		return overlay.AdmittanceInstructions{OutputsToAdmit: []uint32{}, CoinsToRetain: retain}
	}

	// One refusal per object, with the most specific reason found: a
	// carrier's own reason when there was a record output, else whatever
	// the token pass saw, else "not a PushDrop at all".
	reason := carrierReason
	if reason == refuseNotPushDrop && tokenReason != "" {
		reason = tokenReason
	}
	return refuse(reason, txid)
}

// IdentifyNeededInputs implements module.InputIdentifier: nothing is
// needed, since each object is admitted on its own validity.
func (*TopicManager) IdentifyNeededInputs(context.Context, *transaction.Beef, *chainhash.Hash) ([]*transaction.Outpoint, error) {
	return nil, nil
}

// GetDocumentation implements module.TopicManager.
func (*TopicManager) GetDocumentation() string {
	return strings.Join([]string{
		"# tm_finger",
		"",
		"Admits state tokens (PushDrop [tag, C] whose field signature verifies),",
		"carriers (an unmineable transaction with one record output whose",
		"record decodes, whose lock is the identity's record key and whose",
		"signature verifies), and funding outputs (PushDrop [ \"bf\" + 0x02 ]",
		"with no signature). Each object is admitted on its own validity; the",
		"join, the chain rules and the kill switch are ls_finger's. A",
		"transaction that admits nothing but spends held outputs (the sweep of",
		"a funding tree) is accepted for its inputs alone.",
	}, "\n")
}

// GetMetaData implements module.TopicManager.
func (*TopicManager) GetMetaData() *overlay.MetaData {
	return &overlay.MetaData{Name: Topic, Description: "Admits committed-record state tokens, carriers and funding outputs."}
}
