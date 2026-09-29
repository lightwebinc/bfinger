/**
 * Token and carrier decoding against the Go golden: the bytes the Go SDK
 * minted, read by this SDK, with every field checked against what the golden
 * states rather than against this decoder's own output.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { LockingScript, PushDrop, Transaction, Utils } from '@bsv/sdk'
import { CborMap, encode } from '@lightwebinc/bcommon'
import { LockTime, commitment, decodeCarrier, inspectCarrierScript } from './carrier.js'
import { KindUpdate } from './record.js'
import { decodeToken, inspectToken, Tag } from './token.js'
import { golden, parsed, toHex } from './testutil.js'

test('the golden token decodes: tag, commitment, profile lock, valid signature', () => {
  const g = golden()
  const t = decodeToken(LockingScript.fromHex(g.token1ScriptHex))
  assert.ok(t !== undefined)
  assert.equal(toHex(t.c), g.carrier1CHex, 'C is the carrier txid in hash order')
  assert.equal(t.lockingKey.toString(), g.profileLockingKeyHex)
  assert.equal(t.valid, true)
  assert.deepEqual([...Tag], [0x62, 0x66, 0x01])

  const t2 = decodeToken(LockingScript.fromHex(g.token2ScriptHex))
  assert.ok(t2 !== undefined)
  assert.equal(toHex(t2.c), g.carrier2CHex)
  assert.equal(t2.valid, true)
})

test('a token with a flipped signature byte decodes as invalid', () => {
  const g = golden()
  const t = decodeToken(LockingScript.fromHex(g.mutations.token1BadSigScriptHex))
  assert.ok(t !== undefined, 'the shape is still a token')
  assert.equal(toHex(t.c), g.carrier1CHex)
  assert.equal(t.valid, false)
})

test('what is not a token', () => {
  const g = golden()
  // The tag, one byte off. The script pushes it as a direct 3-byte push.
  const badTag = g.token1ScriptHex.replace('03626601', '03626602')
  assert.notEqual(badTag, g.token1ScriptHex)
  assert.equal(inspectToken(LockingScript.fromHex(badTag)).kind, 'bad-tag')
  assert.equal(decodeToken(LockingScript.fromHex(badTag)), undefined)
  // P2PKH is not a PushDrop at all.
  assert.equal(inspectToken(LockingScript.fromHex('76a914' + '00'.repeat(20) + '88ac')).kind, 'not-pushdrop')
  assert.equal(inspectToken(new LockingScript([])).kind, 'not-pushdrop')
  // A carrier output is a PushDrop with two fields, not a token.
  const c1 = Transaction.fromHex(g.carrier1TxHex)
  assert.equal(inspectToken(c1.outputs[0]!.lockingScript).kind, 'not-token')
})

test('the golden carriers decode with the commitment the tokens carry', () => {
  const { g, carrier1, carrier2 } = parsed()
  assert.equal(toHex(commitment(carrier1)), g.carrier1CHex, 'tx.hash(), not the reversed id')
  assert.notEqual(carrier1.id('hex'), g.carrier1CHex)

  const c = decodeCarrier(carrier1)
  assert.ok(typeof c !== 'string', `refused: ${String(c)}`)
  assert.equal(c.outputIndex, 0)
  assert.equal(toHex(c.recordBytes), g.carrier1RecordHex)
  assert.equal(toHex(c.c), g.carrier1CHex)
  assert.equal(c.lockingKey.toString(), g.recordLockingKeyHex)
  assert.equal(c.record.seq, 1n)
  assert.equal(toHex(c.record.identityKey), g.identityKeyHex)
  assert.equal(carrier1.lockTime, LockTime)
  assert.equal(carrier1.inputs[0]?.sequence, 0)

  const c2 = decodeCarrier(carrier2)
  assert.ok(typeof c2 !== 'string', `refused: ${String(c2)}`)
  assert.equal(toHex(c2.recordBytes), g.carrier2RecordHex)
  assert.equal(c2.record.kind, KindUpdate)
  assert.equal(c2.record.seq, 2n)
  assert.equal(toHex(c2.record.prev), g.carrier1CHex, 'prev names the first carrier')
  assert.equal(toHex(c2.record.prevWitness ?? new Uint8Array()), g.witness1Hex, 'the update reveals the first witness')
})

test('a mineable carrier is refused', () => {
  const g = golden()
  const m = Transaction.fromHex(g.mutations.carrier1MineableTxHex)
  assert.equal(m.inputs[0]?.sequence, 0xffffffff)
  assert.equal(decodeCarrier(m), 'mineable')
  // The same transaction with the locktime below the frozen value.
  const low = Transaction.fromHex(g.carrier1TxHex)
  low.lockTime = LockTime - 1
  assert.equal(decodeCarrier(low), 'mineable')
})

test('a carrier with a broken lock or signature is refused by reason', () => {
  const g = golden()
  const withScript = (edit: (chunks: LockingScript['chunks']) => void): Transaction => {
    const tx = Transaction.fromHex(g.carrier1TxHex)
    const out = tx.outputs[0]!
    const chunks = out.lockingScript.chunks.map((c) => ({ ...c, data: c.data === undefined ? undefined : [...c.data] }))
    edit(chunks)
    out.lockingScript = new LockingScript(chunks)
    return tx
  }
  // Flip a byte inside the DER signature (chunk 3: key, CHECKSIG, S, sig, 2DROP).
  const badSig = withScript((chunks) => {
    const sig = chunks[3]?.data
    assert.ok(sig !== undefined && sig.length > 10)
    sig[10] = (sig[10] ?? 0) ^ 0x01
  })
  assert.equal(decodeCarrier(badSig), 'bad-sig')
  // Lock the record output to the profile key instead of the record key.
  const badLock = withScript((chunks) => {
    chunks[0]!.data = Utils.toArray(g.profileLockingKeyHex, 'hex')
  })
  assert.equal(decodeCarrier(badLock), 'bad-lock')
  // Not a carrier at all: the token transaction.
  assert.equal(decodeCarrier(Transaction.fromHex(g.token1TxHex)), 'not-pushdrop')
  // A record output whose record fails to decode refuses the whole carrier.
  const badRecord = withScript((chunks) => {
    const s = chunks[2]?.data
    assert.ok(s !== undefined)
    s[1] = 0xff // key 0 becomes a non-canonical / out-of-order item
  })
  assert.equal(decodeCarrier(badRecord), 'bad-record')
})

test('inspectCarrierScript tells a record output from anything else', () => {
  const g = golden()
  const c1 = Transaction.fromHex(g.carrier1TxHex)
  assert.equal(inspectCarrierScript(c1.outputs[0]!.lockingScript).kind, 'carrier')
  assert.equal(inspectCarrierScript(LockingScript.fromHex(g.token1ScriptHex)).kind, 'not-record')
  assert.equal(inspectCarrierScript(LockingScript.fromHex('76a914' + '00'.repeat(20) + '88ac')).kind, 'not-record')
  // A two-field PushDrop whose first field is a map with an unknown magic is
  // not a record output, rather than a bad one.
  const d = PushDrop.decode(c1.outputs[0]!.lockingScript, 'before')
  const s = [...(d.fields[0] ?? [])]
  // magic is key 0's value, the bytes 44 62 66 72 01: flip the version byte.
  const at = s.findIndex((_, i) => s[i] === 0x44 && s[i + 1] === 0x62 && s[i + 2] === 0x66 && s[i + 3] === 0x72)
  assert.ok(at > 0)
  s[at + 4] = 0x02
  const chunks = c1.outputs[0]!.lockingScript.chunks.map((c) => ({ ...c }))
  chunks[2] = { op: chunks[2]!.op, data: s }
  assert.equal(inspectCarrierScript(new LockingScript(chunks)).kind, 'not-record')
})

// Another application's two-field PushDrop is skipped, not refused. A first
// field too short or not a map never reaches the record decoder, and a map
// the decoder refuses for its shape (text keys) is another payload, not a bad
// record. So a carrier with such an output beside its record output still
// decodes, as the carrier.
test('another payload beside the record output is skipped, not a bad record', () => {
  const g = golden()
  const c1 = Transaction.fromHex(g.carrier1TxHex)
  const others: Array<[string, number[]]> = [
    ['a short map', [0xa1, 0x00, 0x00]],
    ['not a map', [0x00, 0x00]],
    ['a map with text keys', [...encode(new CborMap([{ key: 'a', val: 'bbbbbb' }]))]],
  ]
  for (const [name, field] of others) {
    const chunks = c1.outputs[0]!.lockingScript.chunks.map((c) => ({ ...c }))
    chunks[2] = { op: field.length, data: field }
    const script = new LockingScript(chunks)
    assert.equal(inspectCarrierScript(script).kind, 'not-record', name)
    const tx = Transaction.fromHex(g.carrier1TxHex)
    tx.addOutput({ lockingScript: script, satoshis: 1 })
    const c = decodeCarrier(tx)
    assert.ok(typeof c !== 'string', `${name}: the carrier was refused as ${String(c)}`)
    assert.equal(c.outputIndex, 0, name)
    assert.equal(toHex(c.recordBytes), toHex(PushDrop.decode(c1.outputs[0]!.lockingScript, 'before').fields[0]!), name)
  }
})
