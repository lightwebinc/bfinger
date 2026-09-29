/**
 * The finger key derivations (BRC-42/43).
 *
 * FROZEN AT THE FIRST MINT, together with the token tag, the record magic,
 * the CBOR key map, the locktime value and the RFC 6962 prefixes
 * (docs/committed-record.md section 11). The protocol triple is hashed into
 * every derived key, so changing any part of it orphans every record ever
 * published.
 *
 * The reader's side of the derivation is the library's readerLockingKey
 * (@lightwebinc/bcommon): from the anyone root, the identity's key under the
 * protocol and key id, forSelf=false, which equals what the identity's own
 * wallet produces with counterparty anyone and forSelf=true (measured in
 * bfinger's tools/mintprobe; the Go side in token/token.go makes the same
 * call). What is finger's is the triple.
 */
import type { PublicKey, WalletProtocol } from '@bsv/sdk'
import { readerLockingKey } from '@lightwebinc/bcommon'

/** Security level 1, name "bfinger": invoice numbers "1-bfinger-<keyID>". */
export const Protocol: WalletProtocol = [1, 'bfinger']

/** Locks the state token. */
export const KeyIDProfile = 'profile'

/** Locks the carrier's record output and the funding outputs it spends. */
export const KeyIDRecord = 'record'

/**
 * Throws when identityKeyHex does not parse as a public key. The SDK's parser
 * is lenient about the point itself, so callers treat a throw as one refusal
 * among others rather than as the whole of key validation.
 */
export function profileLockingKey(identityKeyHex: string): PublicKey {
  return readerLockingKey(Protocol, KeyIDProfile, identityKeyHex)
}

/** As profileLockingKey, under the record key id. */
export function recordLockingKey(identityKeyHex: string): PublicKey {
  return readerLockingKey(Protocol, KeyIDRecord, identityKeyHex)
}
