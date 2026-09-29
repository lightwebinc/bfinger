/**
 * `ls_finger`: the join, the chain rules, the three-outpoint answer, pending
 * tokens, the kill switch, and a restore that rebuilds the same answer. The
 * engine is simulated by calling the callbacks in the order Engine.submit
 * does: a previous output is reported spent BEFORE the new outputs are
 * admitted, and each admitted output is reported with the whole
 * transaction as atomic BEEF.
 *
 * The golden carries a create, one update, and a sweep of the funding tree.
 * The other transitions (a bad-witness pair, a sequence skip, a rotation, a
 * retirement, an update under a retired or rotated key) are minted here
 * under the golden's TEST-ONLY private key, and chained onto the golden's
 * carriers and witnesses, so every refused join is refused by the rule
 * under test and not by an unrelated one.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { PrivateKey, Transaction, UnlockingScript } from '@bsv/sdk'
import { commitment, decodeCarrier } from './carrier.js'
import { CborMap } from '@lightwebinc/bcommon'
import { FingerLookupService, JoinFailures, KillReasons, reverseHex } from './ls_finger.js'
import { KindRetire, KindRotate, KindSub, KindUpdate } from './record.js'
import {
  atomicOf,
  beefOf,
  countingHost,
  fakeStorage,
  fill,
  fromHex,
  minter,
  mintCarrier,
  mintToken,
  parsed,
  record,
  row,
  sha256,
  spends,
  toHex,
  type CountingHost,
  type Parsed,
} from './testutil.js'

/** One admission, as Engine.notifyOutputAdmitted reports it in whole-tx mode. */
function admit(ls: FingerLookupService, tx: Transaction, outputIndex = 0): void {
  if (tx.outputs[outputIndex] === undefined) throw new Error('no such output')
  ls.outputAdmittedByTopic({ mode: 'whole-tx', atomicBEEF: atomicOf(tx), outputIndex, topic: 'tm_finger' })
}

/** Every output of the transaction admitted, as the engine does for the funding tree. */
function admitAll(ls: FingerLookupService, tx: Transaction): void {
  for (let i = 0; i < tx.outputs.length; i++) admit(ls, tx, i)
}

function spend(ls: FingerLookupService, spent: Transaction, by: Transaction, outputIndex = 0): void {
  ls.outputSpent({ mode: 'txid', txid: spent.id('hex'), outputIndex, topic: 'tm_finger', spendingTxid: by.id('hex') })
}

/**
 * A distinct funding outpoint for a minted carrier, never admitted here. In
 * reality every carrier spends its own funding output; two carriers on one
 * output are a second spend, which is a kill (tested as such below).
 */
const fundingAt = (n: number): { txid: string; vout: number } => ({ txid: 'f'.repeat(62) + n.toString(16).padStart(2, '0'), vout: 0 })

/** Report the outpoint a minted carrier spends as spent by `by`. */
function spendInput(ls: FingerLookupService, carrier: Transaction, by: Transaction): void {
  const input = carrier.inputs[0]!
  ls.outputSpent({ mode: 'txid', txid: input.sourceTXID!, outputIndex: input.sourceOutputIndex, topic: 'tm_finger', spendingTxid: by.id('hex') })
}

async function ask(ls: FingerLookupService, identityKey: string, pending = false): Promise<string[]> {
  const f = await ls.lookup({ service: 'ls_finger', query: pending ? { identityKey, pending: true } : { identityKey } })
  return f.map((o) => `${o.txid}.${o.outputIndex}`)
}

const key = (tx: Transaction, i = 0): string => `${tx.id('hex')}.${i}`

/** Create and update from the golden, in the engine's callback order. */
function createThenUpdate(ls: FingerLookupService, p: Parsed): void {
  admit(ls, p.token1)
  admit(ls, p.carrier1)
  spend(ls, p.token1, p.token2)
  admit(ls, p.token2)
  admit(ls, p.carrier2)
}

/**
 * The same with the funding tree admitted first and every spend of a
 * funding output reported, as the engine does once the tree is in storage:
 * the carriers' inputs, and the tokens' fee inputs.
 */
function fundedCreateThenUpdate(ls: FingerLookupService, p: Parsed): void {
  admitAll(ls, p.funding)
  spend(ls, p.funding, p.token1, 2)
  admitAll(ls, p.token1)
  spend(ls, p.funding, p.carrier1, 0)
  admit(ls, p.carrier1)
  spend(ls, p.token1, p.token2)
  spend(ls, p.funding, p.token2, 3)
  admitAll(ls, p.token2)
  spend(ls, p.funding, p.carrier2, 1)
  admit(ls, p.carrier2)
}

function fresh(): { host: CountingHost; ls: FingerLookupService; p: Parsed } {
  const host = countingHost()
  return { host, ls: new FingerLookupService(host), p: parsed() }
}

function noFailures(host: CountingHost): void {
  for (const reason of JoinFailures) assert.equal(host.count('finger_join_failures_total', { reason }), 0, reason)
}

const empty = fakeStorage([])

test('presets every join failure and kill reason and exposes sizes', () => {
  const { host, ls } = fresh()
  noFailures(host)
  for (const reason of KillReasons) assert.equal(host.count('finger_killed_total', { reason }), 0, reason)
  assert.equal(ls.size, 0)
  assert.equal(ls.pendingTokens, 0)
  assert.equal(ls.identityCount, 0)
  assert.equal(ls.killedCount, 0)
  assert.equal(ls.fundingCount, 0)
})

test('a create joins from either arrival order and answers two outpoints', async () => {
  for (const carrierFirst of [false, true]) {
    const { host, ls, p } = fresh()
    if (carrierFirst) {
      admit(ls, p.carrier1)
      assert.deepEqual(await ask(ls, p.g.identityKeyHex), [], 'a carrier alone is not a state')
      admit(ls, p.token1)
    } else {
      admit(ls, p.token1)
      assert.equal(ls.pendingTokens, 1, 'a token with no carrier is pending')
      assert.deepEqual(await ask(ls, p.g.identityKeyHex), [])
      assert.deepEqual(await ask(ls, p.g.identityKeyHex, true), [key(p.token1)], 'pending lists the token')
      admit(ls, p.carrier1)
    }
    assert.equal(ls.pendingTokens, 0)
    assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(p.token1), key(p.carrier1)], `carrierFirst=${carrierFirst}`)
    assert.equal(ls.identityCount, 1)
    // The carrier named its funding outpoint whether or not the tree was admitted.
    assert.equal(ls.fundingCount, 1)
    noFailures(host)
  }
})

test('the golden update joins, with the witness revealed, and answers three outpoints', async () => {
  const { host, ls, p } = fresh()
  createThenUpdate(ls, p)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(p.token2), key(p.carrier2), key(p.carrier1)])
  noFailures(host)
  // The identity key is accepted in either case.
  assert.deepEqual(await ask(ls, p.g.identityKeyHex.toUpperCase()), [key(p.token2), key(p.carrier2), key(p.carrier1)])
  // The witness the update revealed hashes to the create's commitment: the
  // fact the join checked, stated once here against the golden itself.
  const c1 = decodeCarrier(p.carrier1)
  assert.ok(typeof c1 !== 'string')
  assert.equal(toHex(c1.record.wc), toHex(sha256(fromHex(p.g.witness1Hex))))
})

test('an update whose token arrives before the create is joined once the create lands', async () => {
  const { host, ls, p } = fresh()
  spend(ls, p.token1, p.token2)
  admit(ls, p.token2)
  admit(ls, p.carrier2)
  assert.equal(host.count('finger_join_failures_total', { reason: 'no-current' }), 1)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [])
  admit(ls, p.token1)
  admit(ls, p.carrier1)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(p.token2), key(p.carrier2), key(p.carrier1)])
})

// A host that catches up from a peer is sent unspent outputs only: both
// carriers, but only the current token, because token1 was spent by token2.
// Live, the create never joins without its token and the update is refused
// no-current; the scheduled rebuild walks the chain as restore does.
test('a host fed unspent outputs only rebuilds the chain once the burst settles', async () => {
  const { host, ls, p } = fresh()
  ls.rebuildDelayMs = 10
  admit(ls, p.carrier1)
  admit(ls, p.token2)
  admit(ls, p.carrier2)
  assert.equal(host.count('finger_join_failures_total', { reason: 'no-current' }), 1)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [])
  await new Promise((r) => setTimeout(r, 50))
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(p.token2), key(p.carrier2), key(p.carrier1)])
  // The rebuild's own walk is not a second refusal.
  assert.equal(host.count('finger_join_failures_total', { reason: 'no-current' }), 1)
  assert.ok(host.logged.some((l) => l.msg === 'ls_finger rebuilt its chains from the index'))
})

// Two conflicting transitions at one sequence: the live join takes the one
// whose token spent the current token, and a rebuild must keep that one even
// when the other sorts first by commitment.
test('a rebuild keeps the transition whose token spent the chain, not the lower commitment', async () => {
  const { ls, p } = fresh()
  const m = minter(p.g.privateKeyHex)
  createThenUpdate(ls, p)
  const seq3 = (b: number) =>
    record({
      identityKey: fromHex(p.g.identityKeyHex),
      seq: 3n,
      kind: KindUpdate,
      prev: fromHex(p.g.carrier2CHex),
      prevWitness: fromHex(p.g.witness2Hex),
      wc: sha256(fill(b)),
    })
  const cA = await mintCarrier(m, seq3(0x77), fundingAt(4))
  const tA = await mintToken(m, commitment(cA), [{ txid: p.token2.id('hex'), vout: 0 }])
  spend(ls, p.token2, tA)
  admit(ls, tA)
  admit(ls, cA)
  const want = await ask(ls, p.g.identityKeyHex)
  assert.equal(want[1], key(cA))
  // A rival at seq 3 whose commitment sorts before cA's, and whose token
  // spent nothing the chain holds.
  let cB = cA
  for (let b = 0x10; ; b++) {
    cB = await mintCarrier(m, seq3(b), fundingAt(5))
    if (toHex(commitment(cB)) < toHex(commitment(cA))) break
  }
  const tB = await mintToken(m, commitment(cB), [{ txid: p.funding.id('hex'), vout: 3 }])
  admit(ls, tB)
  admit(ls, cB)
  ls.rebuildChains()
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), want)
})

// A host that caught up from a peer may hold a sweep without the funding
// outputs it spent, so the engine reports no spend. The kill must come from
// the sweep's own inputs, in either arrival order.
test('a sweep kills from its own inputs, whether it arrives before or after the carriers', async () => {
  const sweepOf = (...carriers: Transaction[]): Transaction => {
    const tx = new Transaction()
    for (const c of carriers) {
      const input = c.inputs[0]!
      tx.addInput({ sourceTXID: input.sourceTXID!, sourceOutputIndex: input.sourceOutputIndex, sequence: 0xffffffff, unlockingScript: new UnlockingScript([]) })
    }
    // The tombstone is funding-shaped, which is what gets it admitted.
    tx.addOutput({ lockingScript: p0.funding.outputs[0]!.lockingScript, satoshis: 1 })
    return tx
  }
  const p0 = parsed()
  for (const sweepFirst of [false, true]) {
    const { ls, p } = fresh()
    const sweep = sweepOf(p.carrier1, p.carrier2)
    if (sweepFirst) admit(ls, sweep)
    createThenUpdate(ls, p)
    if (!sweepFirst) {
      assert.equal((await ask(ls, p.g.identityKeyHex)).length, 3)
      admit(ls, sweep)
    }
    assert.deepEqual(await ask(ls, p.g.identityKeyHex), [], sweepFirst ? 'sweep first' : 'sweep last')
  }
})

test('a rebuild leaves a live chain as it was', async () => {
  const { ls, p } = fresh()
  createThenUpdate(ls, p)
  const want = await ask(ls, p.g.identityKeyHex)
  ls.rebuildChains()
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), want)
})

test('a bad witness is refused and the previous state stays current', async () => {
  const { host, ls, p } = fresh()
  const m = minter(p.g.privateKeyHex)
  admit(ls, p.token1)
  admit(ls, p.carrier1)
  // A token that commits to the bad-witness carrier and spends token1, as a
  // publisher who lost the witness would have to mint.
  const bad = Transaction.fromHex(p.g.mutations.carrier2BadWitnessTxHex)
  const badToken = await mintToken(m, commitment(bad), [{ txid: p.token1.id('hex'), vout: 0 }])
  spend(ls, p.token1, badToken)
  admit(ls, badToken)
  admit(ls, bad)
  assert.equal(host.count('finger_join_failures_total', { reason: 'bad-witness' }), 1)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(p.token1), key(p.carrier1)], 'the create stays current')
  assert.equal(ls.identityCount, 1)
})

test('a sequence skip is refused', async () => {
  const { host, ls, p } = fresh()
  const m = minter(p.g.privateKeyHex)
  createThenUpdate(ls, p)
  const skip = record({
    identityKey: fromHex(p.g.identityKeyHex),
    seq: 4n,
    kind: KindUpdate,
    prev: fromHex(p.g.carrier2CHex),
    prevWitness: fromHex(p.g.witness2Hex),
    wc: sha256(fill(0x77)),
  })
  const carrier3 = await mintCarrier(m, skip, fundingAt(1))
  const token3 = await mintToken(m, commitment(carrier3), [{ txid: p.token2.id('hex'), vout: 0 }])
  spend(ls, p.token2, token3)
  admit(ls, token3)
  admit(ls, carrier3)
  assert.equal(host.count('finger_join_failures_total', { reason: 'bad-seq' }), 1)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(p.token2), key(p.carrier2), key(p.carrier1)])
})

test('a wrong prev, an unspent previous token, and a second create are each refused', async () => {
  const { host, ls, p } = fresh()
  const m = minter(p.g.privateKeyHex)
  createThenUpdate(ls, p)

  // prev names the create instead of the update.
  const wrongPrev = record({
    identityKey: fromHex(p.g.identityKeyHex),
    seq: 3n,
    kind: KindUpdate,
    prev: fromHex(p.g.carrier1CHex),
    prevWitness: fromHex(p.g.witness2Hex),
    wc: sha256(fill(0x77)),
  })
  const c3 = await mintCarrier(m, wrongPrev, fundingAt(1))
  const t3 = await mintToken(m, commitment(c3), [{ txid: p.token2.id('hex'), vout: 0 }])
  spend(ls, p.token2, t3)
  admit(ls, t3)
  admit(ls, c3)
  assert.equal(host.count('finger_join_failures_total', { reason: 'bad-prev' }), 1)

  // A correct record whose token did NOT spend the current token: no spend
  // notification named it.
  const { ls: ls2, host: host2 } = fresh()
  createThenUpdate(ls2, p)
  const good = record({
    identityKey: fromHex(p.g.identityKeyHex),
    seq: 3n,
    kind: KindUpdate,
    prev: fromHex(p.g.carrier2CHex),
    prevWitness: fromHex(p.g.witness2Hex),
    wc: sha256(fill(0x77)),
  })
  const c3b = await mintCarrier(m, good, fundingAt(2))
  const t3b = await mintToken(m, commitment(c3b), [{ txid: p.funding.id('hex'), vout: 3 }])
  admit(ls2, t3b)
  admit(ls2, c3b)
  assert.equal(host2.count('finger_join_failures_total', { reason: 'not-spent' }), 1)
  assert.deepEqual(await ask(ls2, p.g.identityKeyHex), [key(p.token2), key(p.carrier2), key(p.carrier1)])

  // A second create for a known identity.
  const again = await mintCarrier(m, record({ identityKey: fromHex(p.g.identityKeyHex), seq: 1n, kind: 1, wc: sha256(fill(0x01)) }), fundingAt(3))
  const againToken = await mintToken(m, commitment(again), [{ txid: p.funding.id('hex'), vout: 3 }])
  admit(ls2, againToken)
  admit(ls2, again)
  assert.equal(host2.count('finger_join_failures_total', { reason: 'duplicate-create' }), 1)
  assert.deepEqual(await ask(ls2, p.g.identityKeyHex), [key(p.token2), key(p.carrier2), key(p.carrier1)])
})

test('a token locked to the wrong key never joins', async () => {
  const { host, ls, p } = fresh()
  // A different key mints a token committing to the golden create carrier.
  const other = minter(PrivateKey.fromRandom().toHex())
  const rogue = await mintToken(other, Array.from(fromHex(p.g.carrier1CHex)), [{ txid: p.funding.id('hex'), vout: 3 }])
  admit(ls, rogue)
  admit(ls, p.carrier1)
  assert.equal(host.count('finger_join_failures_total', { reason: 'bad-lock' }), 1)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [])
})

test('rotate hands the chain to the successor; the old key answers the rotation', async () => {
  const { host, ls, p } = fresh()
  const m = minter(p.g.privateKeyHex)
  const succ = minter(PrivateKey.fromHex('11'.repeat(32)).toHex())
  createThenUpdate(ls, p)

  const w3 = fill(0x33)
  const rotate = record({
    identityKey: fromHex(p.g.identityKeyHex),
    seq: 3n,
    kind: KindRotate,
    prev: fromHex(p.g.carrier2CHex),
    prevWitness: fromHex(p.g.witness2Hex),
    wc: sha256(w3),
    successor: fromHex(succ.identityKeyHex),
  })
  // The rotation record itself is signed by the current key, and its token
  // is under the current key's profile derivation.
  const c3 = await mintCarrier(m, rotate, fundingAt(1))
  const t3 = await mintToken(m, commitment(c3), [{ txid: p.token2.id('hex'), vout: 0 }])
  spend(ls, p.token2, t3)
  admit(ls, t3)
  admit(ls, c3)
  noFailures(host)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(t3), key(c3), key(p.carrier2)], 'the old key answers the rotation record')
  assert.deepEqual(await ask(ls, succ.identityKeyHex), [key(t3), key(c3), key(p.carrier2)], 'so does the successor, until it publishes')

  // The next record is the successor's: its identity key, its record
  // derivation on the carrier, its profile derivation on the token.
  const w4 = fill(0x44)
  const next = record({
    identityKey: fromHex(succ.identityKeyHex),
    seq: 4n,
    kind: KindUpdate,
    prev: Uint8Array.from(commitment(c3)),
    prevWitness: w3,
    wc: sha256(w4),
  })
  const c4 = await mintCarrier(succ, next, fundingAt(2))
  const t4 = await mintToken(succ, commitment(c4), [{ txid: t3.id('hex'), vout: 0 }])
  spend(ls, t3, t4)
  admit(ls, t4)
  admit(ls, c4)
  noFailures(host)
  assert.deepEqual(await ask(ls, succ.identityKeyHex), [key(t4), key(c4), key(c3)])
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(t3), key(c3), key(p.carrier2)], 'the old key still answers the rotation')

  // An update under the OLD key after the rotation is refused.
  const stale = record({
    identityKey: fromHex(p.g.identityKeyHex),
    seq: 4n,
    kind: KindUpdate,
    prev: Uint8Array.from(commitment(c3)),
    prevWitness: w3,
    wc: sha256(fill(0x55)),
  })
  const c4s = await mintCarrier(m, stale, fundingAt(3))
  const t4s = await mintToken(m, commitment(c4s), [{ txid: t3.id('hex'), vout: 0 }])
  admit(ls, t4s)
  admit(ls, c4s)
  assert.equal(host.count('finger_join_failures_total', { reason: 'rotated' }), 1)

  // Killing the successor's own record kills the successor and removes the
  // rotation's forward pointer; the rotation record itself stands.
  spendInput(ls, c4, p.sweep)
  assert.equal(host.count('finger_killed_total', { reason: 'funding-spent' }), 1)
  assert.deepEqual(await ask(ls, succ.identityKeyHex), [])
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(t3), key(c3), key(p.carrier2)])
  assert.equal(ls.killedCount, 1)
})

test('retire is terminal', async () => {
  const { host, ls, p } = fresh()
  const m = minter(p.g.privateKeyHex)
  createThenUpdate(ls, p)
  const w3 = fill(0x33)
  const retire = record({
    identityKey: fromHex(p.g.identityKeyHex),
    seq: 3n,
    kind: KindRetire,
    prev: fromHex(p.g.carrier2CHex),
    prevWitness: fromHex(p.g.witness2Hex),
    wc: sha256(w3),
    body: new CborMap(),
  })
  const c3 = await mintCarrier(m, retire, fundingAt(1))
  const t3 = await mintToken(m, commitment(c3), [{ txid: p.token2.id('hex'), vout: 0 }])
  spend(ls, p.token2, t3)
  admit(ls, t3)
  admit(ls, c3)
  noFailures(host)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(t3), key(c3), key(p.carrier2)])

  const after = record({
    identityKey: fromHex(p.g.identityKeyHex),
    seq: 4n,
    kind: KindUpdate,
    prev: Uint8Array.from(commitment(c3)),
    prevWitness: w3,
    wc: sha256(fill(0x44)),
  })
  const c4 = await mintCarrier(m, after, fundingAt(2))
  const t4 = await mintToken(m, commitment(c4), [{ txid: t3.id('hex'), vout: 0 }])
  spend(ls, t3, t4)
  admit(ls, t4)
  admit(ls, c4)
  assert.equal(host.count('finger_join_failures_total', { reason: 'retired' }), 1)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(t3), key(c3), key(p.carrier2)])
})

test('the sweep kills the identity; the carrier\'s own spend does not; a late pair cannot resurrect it', async () => {
  const { host, ls, p } = fresh()
  admitAll(ls, p.funding)
  assert.equal(ls.fundingCount, 4)
  // Fee inputs spend funding outputs too; those spends claim no carrier.
  spend(ls, p.funding, p.token1, 2)
  admitAll(ls, p.token1)
  assert.equal(ls.fundingCount, 5, 'the token\'s change is a funding output')
  // The engine reports the carrier's own spend of its funding output before
  // it admits the carrier.
  spend(ls, p.funding, p.carrier1, 0)
  admit(ls, p.carrier1)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(p.token1), key(p.carrier1)])
  noFailures(host)

  // The carrier's own spend, reported again, is not a kill.
  spend(ls, p.funding, p.carrier1, 0)
  assert.equal(host.count('finger_killed_total', { reason: 'funding-spent' }), 0)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(p.token1), key(p.carrier1)])

  // A spend of an outpoint the service never heard of is nothing.
  ls.outputSpent({ mode: 'txid', txid: '22'.repeat(32), outputIndex: 0, topic: 'tm_finger', spendingTxid: p.sweep.id('hex') })
  assert.equal(host.count('finger_killed_total', { reason: 'funding-spent' }), 0)

  // The sweep: the engine reports each funding output spent by the sweep
  // txid, then admits the sweep's funding-shaped output.
  spend(ls, p.funding, p.sweep, 0)
  assert.equal(host.count('finger_killed_total', { reason: 'funding-spent' }), 1)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [])
  assert.deepEqual(await ask(ls, p.g.identityKeyHex, true), [], 'nothing pending for a killed identity either')
  assert.equal(ls.killedCount, 1)
  assert.equal(ls.identityCount, 0)
  assert.equal(ls.carrierCount, 0, 'the killed carrier is dropped')
  assert.equal(ls.tokenCount, 0, 'and its token with it')
  assert.equal(ls.pendingTokens, 0)
  const killedLog = host.logged.filter((l) => l.msg === 'ls_finger killed')
  assert.equal(killedLog.length, 1)
  assert.deepEqual(killedLog[0]?.extra?.['identities'], [p.g.identityKeyHex])
  assert.equal(killedLog[0]?.extra?.['spendingTxid'], p.sweep.id('hex'))
  spend(ls, p.funding, p.sweep, 1)
  spend(ls, p.funding, p.sweep, 3)
  admit(ls, p.sweep)
  assert.equal(host.count('finger_killed_total', { reason: 'funding-spent' }), 1, 'one identity, counted once')

  // A stale token and carrier delivered late are refused, not re-indexed.
  admit(ls, p.token1)
  admit(ls, p.carrier1)
  assert.equal(host.count('finger_join_failures_total', { reason: 'killed' }), 2)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [])
  assert.equal(ls.size, 0)
  assert.equal(ls.killedCount, 1)
})

test('a sweep after the update kills every state in the chain, counted once', async () => {
  const { host, ls, p } = fresh()
  fundedCreateThenUpdate(ls, p)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(p.token2), key(p.carrier2), key(p.carrier1)])
  noFailures(host)
  // carrier2's funding output first: the current state is dropped and with
  // it every carrier of the identity, so carrier1's spend finds nothing.
  spend(ls, p.funding, p.sweep, 1)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [])
  spend(ls, p.funding, p.sweep, 0)
  spend(ls, p.funding, p.sweep, 3)
  assert.equal(host.count('finger_killed_total', { reason: 'funding-spent' }), 1)
  assert.equal(ls.carrierCount, 0)
  assert.equal(ls.tokenCount, 0)
})

test('a carrier whose funding output was already spent by something else is dead on arrival', async () => {
  const { host, ls, p } = fresh()
  admitAll(ls, p.funding)
  spend(ls, p.funding, p.sweep, 0)
  spend(ls, p.funding, p.sweep, 1)
  spend(ls, p.funding, p.sweep, 3)
  admit(ls, p.sweep)
  assert.equal(host.count('finger_killed_total', { reason: 'funding-spent' }), 0, 'nothing to kill yet')
  // The carrier arrives after the sweep (its own spend is still reported:
  // the funding output is retained in storage, so the engine finds it).
  spend(ls, p.funding, p.carrier1, 0)
  admit(ls, p.carrier1)
  assert.equal(host.count('finger_killed_total', { reason: 'funding-spent' }), 1)
  assert.equal(ls.carrierCount, 0)
  admit(ls, p.token1)
  assert.equal(host.count('finger_join_failures_total', { reason: 'killed' }), 1)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [])
  assert.deepEqual(await ask(ls, p.g.identityKeyHex, true), [])
})

test('a second carrier on a claimed funding output kills the first, as the notification would', async () => {
  // The funding tree is NOT admitted here, so no spend is reported and the
  // service learns of the second spend from the carrier itself. (Only the
  // record key can spend a funding output and the engine's SPV step checks
  // that signature, so this is the owner's doing, not an attacker's.)
  const { host, ls, p } = fresh()
  const m = minter(p.g.privateKeyHex)
  admit(ls, p.token1)
  admit(ls, p.carrier1)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(p.token1), key(p.carrier1)])
  const other = minter(PrivateKey.fromHex('22'.repeat(32)).toHex())
  const rival = await mintCarrier(other, record({ identityKey: fromHex(other.identityKeyHex), seq: 1n, kind: 1, wc: sha256(fill(0x02)) }), {
    txid: p.funding.id('hex'),
    vout: 0,
  })
  const rivalToken = await mintToken(other, commitment(rival), [{ txid: p.funding.id('hex'), vout: 3 }])
  admit(ls, rival)
  assert.equal(host.count('finger_killed_total', { reason: 'funding-spent' }), 1)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [])
  admit(ls, rivalToken)
  assert.deepEqual(await ask(ls, other.identityKeyHex), [key(rivalToken), key(rival)], 'the latest claim lives')
  assert.equal(host.count('finger_killed_total', { reason: 'funding-spent' }), 1)
  // Its own spend reported afterwards is not a kill either.
  spend(ls, p.funding, rival, 0)
  assert.equal(host.count('finger_killed_total', { reason: 'funding-spent' }), 1)
  assert.equal(m.identityKeyHex, p.g.identityKeyHex)
})

test('restore rebuilds the same answer from unspent outputs, in any row order', async () => {
  const { ls: live, p } = fresh()
  createThenUpdate(live, p)
  const want = await ask(live, p.g.identityKeyHex)
  assert.equal(want.length, 3)

  // What findUTXOsForTopic returns after the update: token1 is spent and
  // therefore absent; the two carriers and token2 are unspent.
  const rows = [row(p.carrier2, 0), row(p.token2, 0), row(p.carrier1, 0), row(p.token1, 0, true)]
  for (const order of [rows, [...rows].reverse()]) {
    const host = countingHost()
    const ls = new FingerLookupService(host)
    assert.equal(await ls.restore(order, empty), 3)
    assert.deepEqual(await ask(ls, p.g.identityKeyHex), want)
    noFailures(host)
    assert.equal(ls.pendingTokens, 0)
  }

  // A restore is a rebuild, not a merge: restoring again from fewer rows
  // answers the fewer.
  const ls2 = new FingerLookupService(countingHost())
  await ls2.restore(rows, empty)
  assert.equal(await ls2.restore([row(p.carrier1, 0), row(p.token1, 0)], empty), 2)
  assert.deepEqual(await ask(ls2, p.g.identityKeyHex), [key(p.token1), key(p.carrier1)])

  // Rows that are neither are ignored; a corrupt script does not abort.
  const ls3 = new FingerLookupService(countingHost())
  const junk = { ...row(p.token1, 0), outputScript: [0x76, 0xa9] }
  assert.equal(await ls3.restore([junk, ...rows], empty), 3)
})

test('restore keeps a bad witness out of the current state', async () => {
  const { p } = fresh()
  const m = minter(p.g.privateKeyHex)
  const bad = Transaction.fromHex(p.g.mutations.carrier2BadWitnessTxHex)
  const badToken = await mintToken(m, commitment(bad), [{ txid: p.token1.id('hex'), vout: 0 }])
  const host = countingHost()
  const ls = new FingerLookupService(host)
  await ls.restore([row(p.carrier1, 0), row(bad, 0), row(badToken, 0)], empty)
  assert.equal(host.count('finger_join_failures_total', { reason: 'bad-witness' }), 1)
  // token1 was spent by the bad token and so is not among the unspent rows.
  // The create still advances the chain from its carrier, the bad
  // transition does not, and the answer is the create's carrier with no
  // token: what a reader then refuses as NO-TOKEN, which is the truth of it.
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(p.carrier1)])
  assert.equal(ls.pendingTokens, 0, 'the bad token has its carrier; it is refused, not pending')
})

test('restore re-reads each carrier\'s funding outpoint: spent by its carrier is alive, by anything else is killed', async () => {
  const { p } = fresh()
  // The unspent rows after the create, with the funding tree admitted:
  // funding 1 and 3, the token, its change, and the carrier, whose row
  // names the funding outpoint it consumed.
  const rows = [
    row(p.funding, 1),
    row(p.funding, 3),
    row(p.token1, 0),
    row(p.token1, 1),
    row(p.carrier1, 0, false, { outputsConsumed: spends(p.carrier1) }),
  ]
  assert.deepEqual(spends(p.carrier1), [{ txid: p.funding.id('hex'), outputIndex: 0 }])

  // Storage holds funding output 0 spent by the carrier alone: alive.
  const alive = fakeStorage([row(p.funding, 0, true, { consumedBy: [{ txid: p.carrier1.id('hex'), outputIndex: 0 }] })])
  const host = countingHost()
  const ls = new FingerLookupService(host)
  assert.equal(await ls.restore(rows, alive), 5)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(p.token1), key(p.carrier1)])
  assert.equal(host.count('finger_killed_total', { reason: 'funding-spent' }), 0)
  assert.equal(ls.fundingCount, 4, 'three unspent rows and the carrier\'s own')
  noFailures(host)

  // Storage holds it spent by the carrier AND the sweep: killed on restore.
  const swept = fakeStorage([
    row(p.funding, 0, true, {
      consumedBy: [
        { txid: p.carrier1.id('hex'), outputIndex: 0 },
        { txid: p.sweep.id('hex'), outputIndex: 0 },
      ],
    }),
  ])
  const host2 = countingHost()
  const ls2 = new FingerLookupService(host2)
  await ls2.restore(rows, swept)
  assert.deepEqual(await ask(ls2, p.g.identityKeyHex), [])
  assert.equal(host2.count('finger_killed_total', { reason: 'funding-spent' }), 1)
  assert.equal(ls2.killedCount, 1)
  assert.equal(ls2.carrierCount, 0)
  assert.equal(ls2.tokenCount, 0)
  const killedLog = host2.logged.filter((l) => l.msg === 'ls_finger killed')
  assert.equal(killedLog[0]?.extra?.['restoring'], true)
  assert.equal(killedLog[0]?.extra?.['spendingTxid'], p.sweep.id('hex'))
  // And a late pair after the restart is refused, as it would be live.
  admit(ls2, p.token1)
  admit(ls2, p.carrier1)
  assert.equal(host2.count('finger_join_failures_total', { reason: 'killed' }), 2)
  assert.deepEqual(await ask(ls2, p.g.identityKeyHex), [])

  // A carrier admitted while its funding output was not in storage names
  // no consumed outpoint, and storage is not asked.
  let asked = 0
  const counting = { findOutput: async (): Promise<null> => {
    asked++
    return null
  } }
  const ls3 = new FingerLookupService(countingHost())
  await ls3.restore([row(p.token1, 0), row(p.carrier1, 0)], counting)
  assert.equal(asked, 0)
  assert.deepEqual(await ask(ls3, p.g.identityKeyHex), [key(p.token1), key(p.carrier1)])

  // A restore is a rebuild: a kill from the last restore does not outlive
  // storage saying otherwise.
  await ls2.restore(rows, alive)
  assert.equal(ls2.killedCount, 0)
  assert.deepEqual(await ask(ls2, p.g.identityKeyHex), [key(p.token1), key(p.carrier1)])
})

test('an evicted output leaves the index', async () => {
  const { ls, p } = fresh()
  admit(ls, p.token1)
  assert.equal(ls.pendingTokens, 1)
  ls.outputEvicted(p.token1.id('hex'), 0)
  assert.equal(ls.pendingTokens, 0)
  assert.equal(ls.size, 0)
  admit(ls, p.funding, 2)
  assert.equal(ls.fundingCount, 1)
  ls.outputEvicted(p.funding.id('hex'), 2)
  assert.equal(ls.fundingCount, 0)
})

test('the question shape is enforced and the metadata names the service', async () => {
  const { ls } = fresh()
  for (const query of [{}, { identityKey: 'nope' }, { identityKey: '04' + '00'.repeat(32) }, { outpoint: 'a.0' }, undefined]) {
    await assert.rejects(() => ls.lookup({ service: 'ls_finger', query }), /ls_finger: ask/)
  }
  assert.deepEqual(await ls.lookup({ service: 'ls_finger', query: { identityKey: '02' + '11'.repeat(32) } }), [])
  assert.equal((await ls.getMetaData()).name, 'ls_finger')
  assert.match(await ls.getDocumentation(), /^# ls_finger/)
  assert.equal(reverseHex('0102'), '0201')
  assert.equal(ls.admissionMode, 'whole-tx')
  assert.equal(ls.spendNotificationMode, 'txid')
  // A payload of the other mode, or one that does not parse, is ignored.
  ls.outputAdmittedByTopic({ mode: 'whole-tx', atomicBEEF: [1, 2, 3], outputIndex: 0, topic: 'tm_finger' })
  assert.equal(ls.size, 0)
})

test('the module admits through tm_finger what ls_finger then joins', async () => {
  // Round trip through the compiled module entry, as the host would load it.
  const { default: create } = await import('./index.js')
  const host = countingHost()
  const mod = create(host)
  // The tsc build has no inlined library to name, and says so rather than
  // naming the version node_modules happens to hold.
  assert.deepEqual(host.logged, [{ msg: 'finger module built with bcommon', extra: { bcommon: 'unbundled' } }])
  const tm = mod.topics?.['tm_finger']
  const ls = mod.lookups?.['ls_finger'] as FingerLookupService | undefined
  if (tm === undefined || ls === undefined) throw new Error('the module did not mount both')
  const p = parsed()
  const sequence: Array<[Transaction, number[], number[]]> = [
    [p.funding, [], [0, 1, 2, 3]],
    [p.token1, [], [0, 1]],
    [p.carrier1, [0], [0]],
    [p.token2, [0, 1], [0, 1]],
    [p.carrier2, [0], [0]],
  ]
  for (const [tx, prev, want] of sequence) {
    const adm: { outputsToAdmit: number[]; coinsToRetain: number[] } = await tm.identifyAdmissibleOutputs(beefOf(tx), prev)
    assert.deepEqual(adm.outputsToAdmit, want, tx.id('hex'))
    assert.deepEqual(adm.coinsToRetain, prev)
    // Every previous coin is reported spent before the admissions.
    for (const i of prev) {
      const input = tx.inputs[i]!
      ls.outputSpent({ mode: 'txid', txid: input.sourceTXID!, outputIndex: input.sourceOutputIndex, topic: 'tm_finger', spendingTxid: tx.id('hex') })
    }
    for (const i of adm.outputsToAdmit) admit(ls, tx, i)
  }
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [key(p.token2), key(p.carrier2), key(p.carrier1)])
  assert.equal(host.gauges.get('finger_pending_tokens')?.(), 0)
  assert.equal(host.gauges.get('finger_identities')?.(), 1)
  assert.equal(host.gauges.get('finger_killed_identities')?.(), 0)
  assert.equal(typeof ls.restore, 'function')

  // The sweep through the same door.
  const adm = await tm.identifyAdmissibleOutputs(beefOf(p.sweep), [0, 1, 2])
  assert.deepEqual(adm, { outputsToAdmit: [0], coinsToRetain: [0, 1, 2] })
  for (const i of [0, 1, 2]) {
    const input = p.sweep.inputs[i]!
    ls.outputSpent({ mode: 'txid', txid: input.sourceTXID!, outputIndex: input.sourceOutputIndex, topic: 'tm_finger', spendingTxid: p.sweep.id('hex') })
  }
  admit(ls, p.sweep, 0)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), [])
  assert.equal(host.gauges.get('finger_killed_identities')?.(), 1)
  assert.equal(host.gauges.get('finger_identities')?.(), 0)
  assert.equal(host.count('finger_killed_total', { reason: 'funding-spent' }), 1)
})

// A question class is the set of member names, and a charge attaches to a
// class (spec section 14.2). A host that ignored an unknown member would let
// any caller mint a new class whose answer is the base answer unchanged,
// which is a priceable alias of a free question.
test('an unknown query member is refused, never ignored', async () => {
  const { ls, p } = fresh()
  createThenUpdate(ls, p)
  await assert.rejects(
    () => ls.lookup({ service: 'ls_finger', query: { identityKey: p.g.identityKeyHex, fast: true } }),
    /unknown query member "fast"/,
  )
  // The three defined shapes still answer.
  for (const query of [
    { identityKey: p.g.identityKeyHex },
    { identityKey: p.g.identityKeyHex, pending: true },
    { identityKey: p.g.identityKeyHex, carrier: '00'.repeat(32) },
  ]) {
    await ls.lookup({ service: 'ls_finger', query })
  }
})

// The carrier class answers one carrier by its commitment, scoped to the
// identity that published it. The host asserts only that it holds this
// carrier under this identity: it has no root-to-member index and cannot say
// whether the carrier belongs to any store.
test('the carrier class answers only for the identity that published it', async () => {
  const { ls, p } = fresh()
  createThenUpdate(ls, p)
  const display = p.carrier1.id('hex')

  const mine = await ls.lookup({
    service: 'ls_finger',
    query: { identityKey: p.g.identityKeyHex, carrier: display },
  })
  assert.deepEqual(
    mine.map((o) => `${o.txid}.${o.outputIndex}`),
    [key(p.carrier1)],
    'the publishing identity gets the outpoint',
  )

  const stranger = '02' + '11'.repeat(32)
  const other = await ls.lookup({ service: 'ls_finger', query: { identityKey: stranger, carrier: display } })
  assert.equal(other.length, 0, 'a carrier under another identity answers nothing')

  const unknown = await ls.lookup({
    service: 'ls_finger',
    query: { identityKey: p.g.identityKeyHex, carrier: 'ab'.repeat(32) },
  })
  assert.equal(unknown.length, 0, 'a carrier this host does not hold answers nothing')

  await assert.rejects(
    () => ls.lookup({ service: 'ls_finger', query: { identityKey: p.g.identityKeyHex, carrier: 'nothex' } }),
    /64-hex txid/,
  )
})

// Selling a record whose owner retracted it on chain is the one failure that
// turns an optional charge into a liability, so a killed identity answers
// nothing on the carrier class too, exactly as on the base class.
test('a killed identity answers nothing on the carrier class', async () => {
  const { ls, p } = fresh()
  fundedCreateThenUpdate(ls, p)
  const display = p.carrier1.id('hex')
  const before = await ls.lookup({
    service: 'ls_finger',
    query: { identityKey: p.g.identityKeyHex, carrier: display },
  })
  assert.equal(before.length, 1, 'held before the sweep')

  spend(ls, p.funding, p.sweep, 0)

  const after = await ls.lookup({
    service: 'ls_finger',
    query: { identityKey: p.g.identityKeyHex, carrier: display },
  })
  assert.equal(after.length, 0, 'a killed identity sells nothing')
})

// A sub-record is a store member, never a transition. The host indexes it,
// answers it by its commitment, drops it with the identity, and never
// advances the chain on it. The restore case is the one that matters: every
// carrier is replayed through advance in commitment order, and a create-
// shaped record sorting before the real create would otherwise take the
// chain start and orphan every transition behind it.
test('a sub-record is indexed and answered, never advances, and survives restore in any position', async () => {
  const { host, ls, p } = fresh()
  const m = minter(p.g.privateKeyHex)
  createThenUpdate(ls, p)
  const before = await ask(ls, p.g.identityKeyHex)
  assert.equal(before.length, 3)

  // Mint sub-records until one sorts BEFORE the real create by commitment,
  // so the restore below replays it first.
  const createC = reverseHex(p.carrier1.id('hex'))
  let sub: Transaction | undefined
  let n = 0
  while (sub === undefined && n < 64) {
    const candidate = await mintCarrier(
      m,
      record({ identityKey: fromHex(p.g.identityKeyHex), seq: 1n, kind: KindSub, wc: sha256(fill(0x60 + n)), salt: fill(0x80 + n),
        body: new CborMap([{ key: 'plan', val: 'a plan in its own carrier' }]) }),
      fundingAt(0x40 + n),
    )
    if (reverseHex(candidate.id('hex')) < createC) sub = candidate
    n++
  }
  assert.ok(sub !== undefined, 'no candidate sorted before the create in 64 tries')

  admit(ls, sub)
  noFailures(host)
  assert.deepEqual(await ask(ls, p.g.identityKeyHex), before, 'the chain answer is unchanged')
  const mine = await ls.lookup({ service: 'ls_finger', query: { identityKey: p.g.identityKeyHex, carrier: sub.id('hex') } })
  assert.deepEqual(mine.map((o) => `${o.txid}.${o.outputIndex}`), [key(sub)], 'the carrier class answers it')

  // Restore with the sub-record's row first in every order tried.
  const rows = [row(sub, 0), row(p.carrier2, 0), row(p.token2, 0), row(p.carrier1, 0)]
  for (const order of [rows, [...rows].reverse()]) {
    const h2 = countingHost()
    const ls2 = new FingerLookupService(h2)
    assert.equal(await ls2.restore(order, empty), 4)
    noFailures(h2)
    assert.deepEqual(await ask(ls2, p.g.identityKeyHex), before, 'restore kept the real chain')
    const again = await ls2.lookup({ service: 'ls_finger', query: { identityKey: p.g.identityKeyHex, carrier: sub.id('hex') } })
    assert.equal(again.length, 1, 'restore kept the sub-record')
  }

  // A kill takes the store with the identity.
  spendInput(ls, p.carrier1, p.sweep)
  const gone = await ls.lookup({ service: 'ls_finger', query: { identityKey: p.g.identityKeyHex, carrier: sub.id('hex') } })
  assert.equal(gone.length, 0, 'a killed identity answers nothing on the carrier class')
})

// A commitment is public and a token is admitted on its own validity, so
// anyone may mint a token over anyone's commitment. The dangerous ordering
// is a stranger's token arriving AFTER the real one and BEFORE the carrier:
// with one token per commitment it displaced the real one, the join then
// failed bad-lock, and nothing ever re-added the real token, so the identity
// stopped advancing at this host permanently.
test('a stranger\'s token over a commitment cannot displace the real one', async () => {
  const { host, ls, p } = fresh()
  const squatter = minter('22'.repeat(32))
  assert.notEqual(squatter.identityKeyHex, p.g.identityKeyHex)

  // The real token, then the squatter's over the SAME commitment, then the
  // carrier. This is the order that used to wedge it.
  admit(ls, p.token1)
  const squat = await mintToken(squatter, commitment(p.carrier1), [{ txid: 'a'.repeat(64), vout: 0 }])
  admit(ls, squat)
  admit(ls, p.carrier1)

  assert.deepEqual(
    await ask(ls, p.g.identityKeyHex),
    [key(p.token1), key(p.carrier1)],
    'the identity advanced on its own token',
  )
  assert.equal(host.count('finger_join_failures_total', { reason: 'bad-lock' }), 0, 'the squat never reached the chain checks')

  // The squatter gets nothing under its own key: it holds a token over
  // somebody else's commitment and no carrier of its own.
  assert.deepEqual(await ask(ls, squatter.identityKeyHex), [])

  // And the same in the other order: squatter first, then the real token.
  const b = fresh()
  const squat2 = await mintToken(squatter, commitment(b.p.carrier1), [{ txid: 'b'.repeat(64), vout: 0 }])
  admit(b.ls, squat2)
  admit(b.ls, b.p.carrier1)
  admit(b.ls, b.p.token1)
  assert.deepEqual(
    await ask(b.ls, b.p.g.identityKeyHex),
    [key(b.p.token1), key(b.p.carrier1)],
    'the real token still joins when it arrives after the squat and the carrier',
  )

  // A restore sees both tokens as rows and must still pick the real one.
  const h3 = countingHost()
  const ls3 = new FingerLookupService(h3)
  await ls3.restore([row(squat, 0), row(p.carrier1, 0), row(p.token1, 0)], empty)
  assert.deepEqual(await ask(ls3, p.g.identityKeyHex), [key(p.token1), key(p.carrier1)], 'restore picked the real token')
  noFailures(h3)
})
