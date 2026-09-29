/**
 * `decodeFunding` against the golden: every funding-shaped output in the
 * golden decodes to the record locking key, and nothing else does, including
 * the near misses PushDrop.decode alone would accept.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { LockingScript, OP, Script, Transaction, Utils } from '@bsv/sdk'
import { decodeFunding, FundingTag } from './funding.js'
import { parsed } from './testutil.js'

test('every golden funding output decodes to the record locking key', () => {
  const { g, funding, token1, token2, sweep } = parsed()
  const outputs: Array<[string, Transaction, number]> = [
    ['funding:0', funding, 0],
    ['funding:1', funding, 1],
    ['funding:2', funding, 2],
    ['funding:3', funding, 3],
    ['token1 change', token1, 1],
    ['token2 change', token2, 1],
    ['sweep:0', sweep, 0],
  ]
  for (const [name, tx, i] of outputs) {
    const key = decodeFunding(tx.outputs[i]!.lockingScript)
    assert.ok(key !== undefined, name)
    assert.equal(key.toString(), g.recordLockingKeyHex, name)
  }
  assert.deepEqual([...FundingTag], [0x62, 0x66, 0x02])
})

test('the token, the carrier and the near misses are not funding outputs', () => {
  const { g, carrier1, token1 } = parsed()
  const key = Utils.toArray(g.recordLockingKeyHex, 'hex')
  const good = new Script([
    { op: key.length, data: key },
    { op: OP.OP_CHECKSIG },
    { op: FundingTag.length, data: [...FundingTag] },
    { op: OP.OP_DROP },
  ])
  assert.ok(decodeFunding(good) !== undefined, 'the constructed shape is the golden shape')

  const miss = (name: string, chunks: Script['chunks']): void => {
    assert.equal(decodeFunding(new Script(chunks)), undefined, name)
  }
  miss('token script', LockingScript.fromHex(g.token1ScriptHex).chunks)
  miss('token output', token1.outputs[0]!.lockingScript.chunks)
  miss('carrier output', carrier1.outputs[0]!.lockingScript.chunks)
  miss('P2PKH', LockingScript.fromHex('76a914' + '00'.repeat(20) + '88ac').chunks)
  miss('bare P2PK', good.chunks.slice(0, 2))
  miss('token tag with one field', [good.chunks[0]!, good.chunks[1]!, { op: 3, data: [0x62, 0x66, 0x01] }, good.chunks[3]!])
  miss('wrong version', [good.chunks[0]!, good.chunks[1]!, { op: 3, data: [0x62, 0x66, 0x03] }, good.chunks[3]!])
  miss('OP_2DROP', [good.chunks[0]!, good.chunks[1]!, good.chunks[2]!, { op: OP.OP_2DROP }])
  miss('OP_CHECKSIGVERIFY', [good.chunks[0]!, { op: OP.OP_CHECKSIGVERIFY }, good.chunks[2]!, good.chunks[3]!])
  miss('trailing chunk', [...good.chunks, { op: OP.OP_TRUE }])
  miss('short key', [{ op: 32, data: key.slice(1) }, good.chunks[1]!, good.chunks[2]!, good.chunks[3]!])
  miss('empty', [])
})
