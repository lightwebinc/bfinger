/**
 * The host admits a token or a carrier only in the one encoding the producer
 * writes, and a record only when the keys it names are canonical: the twin of
 * the Go reader's refusals in internal/reader/verify/canonical_test.go.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { LockingScript, Transaction } from '@bsv/sdk'
import { FingerTopicManager } from './tm_finger.js'
import { KindCreate, KindRotate } from './record.js'
import { beefOf, countingHost, fill, fromHex, mintCarrier, minter, parsed, record, sha256 } from './testutil.js'

/** 02 || p+1: a second encoding of the point with x = 1. */
const aliasKey = fromHex('02' + 'fffffffffffffffffffffffffffffffffffffffffffffffffffffffefffffc30')

/** The first field's push follows the 33-byte key push and OP_CHECKSIG. */
const firstField = (1 + 33 + 1) * 2

/**
 * The script with its first field pushed one size wider than it needs: a
 * direct push as OP_PUSHDATA1, OP_PUSHDATA1 as OP_PUSHDATA2. The SDK's
 * PushDrop decoder reads the same key and fields from it.
 */
function widen(s: LockingScript): LockingScript {
  const h = s.toHex()
  const op = parseInt(h.slice(firstField, firstField + 2), 16)
  const rest = h.slice(firstField + 2)
  if (op >= 1 && op <= 75) return LockingScript.fromHex(h.slice(0, firstField) + '4c' + h.slice(firstField, firstField + 2) + rest)
  if (op === 0x4c) return LockingScript.fromHex(h.slice(0, firstField) + '4d' + rest.slice(0, 2) + '00' + rest.slice(2))
  throw new Error(`widen: opcode ${op}`)
}

async function refusedAs(tx: Transaction, reason: string): Promise<void> {
  const host = countingHost()
  const tm = new FingerTopicManager(host)
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(tx), []), { outputsToAdmit: [], coinsToRetain: [] })
  assert.equal(host.count('finger_refused_total', { reason }), 1, reason)
}

test('a token whose PushDrop is not the minimal encoding is refused as bad-tag', async () => {
  const { g } = parsed()
  const tx = Transaction.fromHex(g.token1TxHex)
  tx.outputs.splice(1)
  tx.outputs[0]!.lockingScript = widen(tx.outputs[0]!.lockingScript)
  await refusedAs(tx, 'bad-tag')
})

test('a carrier whose PushDrop is not the minimal encoding is refused as bad-record', async () => {
  const { carrier1 } = parsed()
  const tx = Transaction.fromHex(carrier1.toHex())
  tx.outputs[0]!.lockingScript = widen(tx.outputs[0]!.lockingScript)
  await refusedAs(tx, 'bad-record')
})

test('a record naming the aliased key as its identity is refused as bad-record', async () => {
  const { g } = parsed()
  const m = minter(g.privateKeyHex)
  const r = record({ identityKey: aliasKey, seq: 1n, kind: KindCreate, wc: sha256(fill(0x51)) })
  await refusedAs(await mintCarrier(m, r, { txid: 'f'.repeat(64), vout: 0 }), 'bad-record')
})

test('a rotation naming the aliased key as its successor is refused as bad-record', async () => {
  const { g } = parsed()
  const m = minter(g.privateKeyHex)
  const r = record({
    identityKey: fromHex(g.identityKeyHex),
    seq: 2n,
    kind: KindRotate,
    prev: fill(0x01),
    prevWitness: fill(0x02),
    wc: sha256(fill(0x52)),
    successor: aliasKey,
  })
  await refusedAs(await mintCarrier(m, r, { txid: 'f'.repeat(64), vout: 0 }), 'bad-record')
})

// The carrier here spends a made-up outpoint with an empty unlocking script,
// so it is refused, but by the input check after the record's rules: the
// successor alone is what the case above turns into bad-record.
test('control: the same rotation to a canonical successor passes the record check', async () => {
  const { g } = parsed()
  const m = minter(g.privateKeyHex)
  const succ = minter('11'.repeat(32))
  const r = record({
    identityKey: fromHex(g.identityKeyHex),
    seq: 2n,
    kind: KindRotate,
    prev: fill(0x01),
    prevWitness: fill(0x02),
    wc: sha256(fill(0x52)),
    successor: fromHex(succ.identityKeyHex),
  })
  await refusedAs(await mintCarrier(m, r, { txid: 'f'.repeat(64), vout: 0 }), 'non-canonical-unlocking')
})
