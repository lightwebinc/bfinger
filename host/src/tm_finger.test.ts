/**
 * `tm_finger` against the golden: admits every golden object (tokens with
 * their funding-shaped change, carriers, the funding tree), refuses each
 * mutation with its reason, accepts the sweep for its inputs, and never
 * throws on garbage. The counters are asserted from the counting host,
 * because a lane refusing everything looks busier than a healthy one and
 * the counter is what tells them apart.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { LockingScript, Transaction } from '@bsv/sdk'
import { AdmitKinds, RefuseReasons, FingerTopicManager } from './tm_finger.js'
import { beefOf, countingHost, parsed } from './testutil.js'

const p2pkh = LockingScript.fromHex('76a914' + '00'.repeat(20) + '88ac')

/** The golden token transaction with only its token output, so a mutation of that output is the whole object. */
function tokenOnly(hex: string): Transaction {
  const tx = Transaction.fromHex(hex)
  tx.outputs.splice(1)
  return tx
}

test('presets every admission kind and refusal reason at zero', () => {
  const host = countingHost()
  new FingerTopicManager(host)
  for (const kind of AdmitKinds) assert.equal(host.count('finger_admitted_total', { kind }), 0, kind)
  for (const reason of RefuseReasons) assert.equal(host.count('finger_refused_total', { reason }), 0, reason)
})

test('admits the golden token and carrier, and the update pair', async () => {
  const host = countingHost()
  const tm = new FingerTopicManager(host)
  const { carrier1, token1, carrier2, token2 } = parsed()

  // A token's change output is a funding output, admitted beside it.
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(token1), []), { outputsToAdmit: [0, 1], coinsToRetain: [] })
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(carrier1), []), { outputsToAdmit: [0], coinsToRetain: [] })
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(carrier2), []), { outputsToAdmit: [0], coinsToRetain: [] })
  // token2 spends token1 at input 0; the engine reports that as a previous
  // coin, and the spent token is RETAINED so a formula's history can walk it.
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(token2), [0]), { outputsToAdmit: [0, 1], coinsToRetain: [0] })

  assert.equal(host.count('finger_admitted_total', { kind: 'token' }), 2)
  assert.equal(host.count('finger_admitted_total', { kind: 'carrier' }), 2)
  assert.equal(host.count('finger_admitted_total', { kind: 'funding' }), 2)
  assert.equal(host.count('finger_admitted_total', { kind: 'spend' }), 0)
  for (const reason of RefuseReasons) assert.equal(host.count('finger_refused_total', { reason }), 0, reason)
})

test('admits the funding tree, retains a carrier\'s funding input, and accepts the sweep for its inputs', async () => {
  const host = countingHost()
  const tm = new FingerTopicManager(host)
  const { funding, carrier1, sweep } = parsed()

  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(funding), []), { outputsToAdmit: [0, 1, 2, 3], coinsToRetain: [] })
  assert.equal(host.count('finger_admitted_total', { kind: 'funding' }), 4)

  // With the tree in storage the engine reports the carrier's funding input
  // as a previous coin; it is retained so the funding output stays in
  // storage marked spent by the carrier, which is what makes a second
  // spend of it visible.
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(carrier1), [0]), { outputsToAdmit: [0], coinsToRetain: [0] })

  // The golden sweep spends funding outputs 0, 1 and 3 (inputs 0, 1, 2) to
  // one funding-shaped output, which is admitted as a funding output; the
  // coins are retained either way.
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(sweep), [0, 1, 2]), { outputsToAdmit: [0], coinsToRetain: [0, 1, 2] })
  assert.equal(host.count('finger_admitted_total', { kind: 'funding' }), 5)
  assert.equal(host.count('finger_admitted_total', { kind: 'spend' }), 0)

  // A sweep to somewhere the topic does not keep (a plain P2PKH) admits no
  // output and is accepted for its inputs alone, counted as a spend, not a
  // refusal: the engine treats consumed previous coins as an accepted
  // submission and reports each as spent.
  const away = Transaction.fromHex(sweep.toHex())
  away.outputs[0]!.lockingScript = p2pkh
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(away), [0, 1, 2]), { outputsToAdmit: [], coinsToRetain: [0, 1, 2] })
  assert.equal(host.count('finger_admitted_total', { kind: 'spend' }), 1)
  for (const reason of RefuseReasons) assert.equal(host.count('finger_refused_total', { reason }), 0, reason)

  // The same transaction spending nothing the topic holds is just not an
  // object, and is refused.
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(away), []), { outputsToAdmit: [], coinsToRetain: [] })
  assert.equal(host.count('finger_refused_total', { reason: 'not-pushdrop' }), 1)
  assert.equal(host.count('finger_admitted_total', { kind: 'spend' }), 1)
})

test('refuses each mutation with its reason, and retains the spent coin anyway', async () => {
  const host = countingHost()
  const tm = new FingerTopicManager(host)
  const { g, token1 } = parsed()

  const mineable = Transaction.fromHex(g.mutations.carrier1MineableTxHex)
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(mineable), []), { outputsToAdmit: [], coinsToRetain: [] })
  assert.equal(host.count('finger_refused_total', { reason: 'mineable' }), 1)

  // A bad token that spends a held coin: refused, and the coin retained.
  // Not a spend-only acceptance, because a token-shaped output WAS there.
  const badSig = tokenOnly(g.token1TxHex)
  badSig.outputs[0]!.lockingScript = LockingScript.fromHex(g.mutations.token1BadSigScriptHex)
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(badSig), [0]), { outputsToAdmit: [], coinsToRetain: [0] })
  assert.equal(host.count('finger_refused_total', { reason: 'bad-sig' }), 1)

  const badTag = tokenOnly(g.token1TxHex)
  badTag.outputs[0]!.lockingScript = LockingScript.fromHex(g.token1ScriptHex.replace('03626601', '03626603'))
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(badTag), []), { outputsToAdmit: [], coinsToRetain: [] })
  assert.equal(host.count('finger_refused_total', { reason: 'bad-tag' }), 1)

  // A transaction whose outputs are neither: a bare P2PKH.
  const plain = tokenOnly(g.token1TxHex)
  plain.outputs[0]!.lockingScript = p2pkh
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(plain), []), { outputsToAdmit: [], coinsToRetain: [] })
  assert.equal(host.count('finger_refused_total', { reason: 'not-pushdrop' }), 1)

  // Garbage is refused, never thrown.
  assert.deepEqual(await tm.identifyAdmissibleOutputs([1, 2, 3], []), { outputsToAdmit: [], coinsToRetain: [] })
  assert.equal(host.count('finger_refused_total', { reason: 'other' }), 1)

  // The good token still admits after all of that.
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(token1), []), { outputsToAdmit: [0, 1], coinsToRetain: [] })
  assert.equal(host.count('finger_admitted_total', { kind: 'token' }), 1)
  assert.equal(host.count('finger_admitted_total', { kind: 'spend' }), 0)
  assert.equal(host.logged.filter((l) => l.msg === 'tm_finger refused').length, 5)
})

test('a transaction with one good token and one bad one admits the good one only', async () => {
  const host = countingHost()
  const tm = new FingerTopicManager(host)
  const { g } = parsed()
  const tx = tokenOnly(g.token1TxHex)
  tx.addOutput({ satoshis: 1, lockingScript: LockingScript.fromHex(g.mutations.token1BadSigScriptHex) })
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(tx), []), { outputsToAdmit: [0], coinsToRetain: [] })
  assert.equal(host.count('finger_admitted_total', { kind: 'token' }), 1)
  assert.equal(host.count('finger_refused_total', { reason: 'bad-sig' }), 0, 'the object was admitted, not refused')
})

test('identifyNeededInputs asks for nothing and the metadata names the topic', async () => {
  const tm = new FingerTopicManager(countingHost())
  assert.deepEqual(await tm.identifyNeededInputs(), [])
  assert.equal((await tm.getMetaData()).name, 'tm_finger')
  assert.match(await tm.getDocumentation(), /^# tm_finger/)
})
