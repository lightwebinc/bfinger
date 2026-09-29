/**
 * The derivations against the Go golden: the cross-SDK proof that a reader
 * holding only the identity key recomputes the same locking keys the Go
 * wallet locked with, under counterparty anyone and forSelf=true.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { KeyIDProfile, KeyIDRecord, Protocol, profileLockingKey, recordLockingKey } from './keys.js'
import { golden } from './testutil.js'

test('the frozen triple is what it says', () => {
  assert.deepEqual(Protocol, [1, 'bfinger'])
  assert.equal(KeyIDProfile, 'profile')
  assert.equal(KeyIDRecord, 'record')
})

test('profile and record locking keys match the Go SDK golden', () => {
  const g = golden()
  assert.equal(profileLockingKey(g.identityKeyHex).toString(), g.profileLockingKeyHex)
  assert.equal(recordLockingKey(g.identityKeyHex).toString(), g.recordLockingKeyHex)
  assert.notEqual(g.profileLockingKeyHex, g.recordLockingKeyHex, 'the two key ids must derive different keys')
  assert.notEqual(g.profileLockingKeyHex, g.identityKeyHex, 'a derived key is never the identity key')
})

test('derivation is deterministic and distinct per identity', () => {
  const g = golden()
  assert.equal(profileLockingKey(g.identityKeyHex).toString(), profileLockingKey(g.identityKeyHex).toString())
  // The anyone key's own child: a different identity, a different lock.
  const other = '0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798'
  assert.notEqual(profileLockingKey(other).toString(), g.profileLockingKeyHex)
  assert.notEqual(recordLockingKey(other).toString(), g.recordLockingKeyHex)
})
