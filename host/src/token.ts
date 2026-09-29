/**
 * The mined state token: PushDrop [tag, C] under the identity's profile
 * derivation, one per transition, each spending the last. Mirrors
 * token/token.go.
 *
 * The token is deliberately terse: a three-byte tag, a 32-byte commitment and
 * Lock's signature over them. The record it commits to rides the plane inside
 * a carrier transaction and never reaches the chain.
 */
import { PushDrop, type LockingScript, type PublicKey, type Script } from '@bsv/sdk'
import { verifyFieldSignature } from '@lightwebinc/bcommon'

// Lock's field signature is checked by the library; it stays exported here
// so the token module's export surface is unchanged.
export { verifyFieldSignature } from '@lightwebinc/bcommon'

/** The token's leading field: "bf" and version 1. Frozen at the first mint. */
export const Tag: readonly number[] = [0x62, 0x66, 0x01]

export interface Token {
  /** The commitment: the carrier's txid in hash byte order, 32 bytes. */
  c: number[]
  lockingKey: PublicKey
  /** Whether the embedded signature verifies under lockingKey. */
  valid: boolean
}

/**
 * What a script is, for the topic manager's refusal accounting:
 * `not-pushdrop` did not decode as a lock-before PushDrop at all;
 * `not-token` is a PushDrop with some other field count (a carrier has two);
 * `bad-tag` has three fields but the wrong tag or a commitment that is not
 * 32 bytes.
 */
export type TokenInspection = { kind: 'token'; token: Token } | { kind: 'not-pushdrop' | 'not-token' | 'bad-tag' }

function sameBytes(a: readonly number[], b: readonly number[]): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i])
}

export function inspectToken(script: Script): TokenInspection {
  let decoded: { lockingPublicKey: PublicKey; fields: number[][] }
  try {
    // Only the lock-before layout decodes, which is the layout Lock writes;
    // a lock-after script from some other producer is refused, not guessed
    // at. decode throws on anything that is not a PushDrop.
    decoded = PushDrop.decode(script as LockingScript, 'before')
  } catch {
    return { kind: 'not-pushdrop' }
  }
  // Tag, commitment, signature: Lock appends the signature as one more field
  // than the two it was given.
  if (decoded.fields.length !== 3) return { kind: 'not-token' }
  const [tag, c, sig] = decoded.fields
  if (tag === undefined || c === undefined || sig === undefined) return { kind: 'not-token' }
  if (!sameBytes(tag, Tag) || c.length !== 32) return { kind: 'bad-tag' }
  return {
    kind: 'token',
    token: { c: [...c], lockingKey: decoded.lockingPublicKey, valid: verifyFieldSignature(decoded.lockingPublicKey, [...tag, ...c], sig) },
  }
}

/** A token, valid or not, or undefined when the script is not a token. */
export function decodeToken(script: Script): Token | undefined {
  const insp = inspectToken(script)
  return insp.kind === 'token' ? insp.token : undefined
}
