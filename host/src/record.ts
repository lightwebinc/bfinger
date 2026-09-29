/**
 * The committed record: docs/committed-record.md section 2, mirroring
 * record/schema.go.
 *
 * The map keys are frozen at the first mint; a new field is a new key, never
 * a renumbering. Unknown keys are preserved by any party that re-encodes a
 * record and ignored by any party that reads one, so a re-encode by an older
 * reader never drops a newer field.
 */
import {
  CborError,
  CborMap,
  type Pair,
  type Value,
  decodeValue,
  encode,
  decodeRefs,
  encodeRefs,
  StoreError,
  type Ref,
} from '@lightwebinc/bcommon'

// The refs entry and its bounds are the library's; the key that carries
// them, and the RecordError they are refused with, are finger's.
export { MaxRefMembers, MaxRefName, MaxRefs } from '@lightwebinc/bcommon'
export type { Ref } from '@lightwebinc/bcommon'

const keyMagic = 0n
const keyIdentityKey = 1n
const keySeq = 2n
const keyKind = 3n
const keyPrev = 4n
const keySalt = 5n
const keyWC = 6n
const keyPrevWitness = 7n
const keyNotBefore = 8n
const keyNotAfter = 9n
const keyBody = 10n
const keyRefs = 11n
const keySuccessor = 12n

export const KindCreate = 1
export const KindUpdate = 2
export const KindRotate = 3
export const KindRetire = 4
/**
 * A sub-record: a store member the primary record commits to through refs.
 * Create-shaped (seq 1, zero prev, no witness reveal) and never a transition:
 * a host indexes it and answers it by its commitment, and never advances an
 * identity's chain on it. Without its own kind it would be a second create,
 * refused live and, on a restart that replays carriers by sequence (ties by
 * commitment),
 * able to win the chain start and orphan every real transition behind it.
 */
export const KindSub = 5
/**
 * A store's manifest: a sub-record whose body lists the store's members in
 * order. It is the head of a store of more than one member and is not itself
 * a member, so the store's root is over what it lists and not over it. Its
 * own kind so that "the thing at the head is a manifest" is checked rather
 * than inferred from the count, and so that it stays visible when the body
 * is sealed.
 */
export const KindManifest = 6
export type Kind = 1 | 2 | 3 | 4 | 5 | 6

/**
 * MaxBodyBytes bounds the encoded body. A profile is delivered to every
 * subscribed host and billed by the byte; an unbounded body is somebody
 * else's bandwidth.
 */
export const MaxBodyBytes = 16384

/**
 * MaxSubBodyBytes bounds the encoded body of a sub-record or a manifest. A
 * record is delivered to every reader of the identity; a store is fetched
 * only by a reader that wants it, so it can carry more. The number is a
 * function of the plane's minimum path MTU: at the 1280-byte IPv6 floor a
 * 64 KiB object is 59 fragments, which is where the repair curve turns.
 */
export const MaxSubBodyBytes = 65536

/** The encoded-body bound for a kind: a store carries more than a transition. */
export function bodyBound(kind: Kind): number {
  return kind === KindSub || kind === KindManifest ? MaxSubBodyBytes : MaxBodyBytes
}

/** MagicV1 is the record's leading field: "bfr" and version 1. */
export const MagicV1: Uint8Array = Uint8Array.from([0x62, 0x66, 0x72, 0x01])

export type RecordErrorCode = 'shape' | 'magic' | 'missing' | 'field' | 'body-size' | 'kind' | 'window' | 'dup-ref'

export class RecordError extends Error {
  constructor(
    readonly code: RecordErrorCode,
    detail?: string,
  ) {
    super(detail === undefined ? `record: ${code}` : `record: ${code}: ${detail}`)
    this.name = 'RecordError'
  }
}

/**
 * The state document. Fixed-width fields are byte arrays of the stated
 * length; optional ones are absent when not present; body keys are text;
 * unknown carries every integer key this version does not define, verbatim.
 */
export interface CommittedRecord {
  magic: Uint8Array
  identityKey: Uint8Array
  seq: bigint
  kind: Kind
  prev: Uint8Array
  salt: Uint8Array
  wc: Uint8Array
  prevWitness?: Uint8Array
  notBefore: bigint
  notAfter: bigint
  body: CborMap
  refs: Ref[]
  successor?: Uint8Array
  unknown: Pair[]
}

export function isZero(b: Uint8Array): boolean {
  for (const x of b) if (x !== 0) return false
  return true
}

function fixed(v: Value | undefined, n: number, name: string): Uint8Array {
  if (!(v instanceof Uint8Array) || v.length !== n) throw new RecordError('field', `${name} wants ${n} bytes`)
  return v.slice()
}

function unsigned(v: Value | undefined, name: string): bigint {
  if (typeof v !== 'bigint' || v < 0n) throw new RecordError('field', `${name} wants an unsigned integer`)
  return v
}

function checkKeyPrefix(k: Uint8Array, name: string): void {
  if (k[0] !== 0x02 && k[0] !== 0x03) throw new RecordError('field', `${name} prefix`)
}

/**
 * A refs-entry refusal as the RecordError finger has always raised for it:
 * the same code and the same detail, so `record: field: ref name` and
 * `record: dup-ref: <name>` read as they did before the codec moved.
 */
function asRecordError<T>(f: () => T): T {
  try {
    return f()
  } catch (err) {
    if (err instanceof StoreError) throw new RecordError(err.code, err.detail)
    throw err
  }
}

/** Body keys must be text, and the encoded body must fit the bound. */
function checkBody(body: Value, kind: Kind): CborMap {
  if (!(body instanceof CborMap)) throw new RecordError('field', 'body')
  for (const p of body.entries) {
    if (typeof p.key !== 'string') throw new RecordError('field', `body key ${typeof p.key}`)
  }
  if (encode(body).length > bodyBound(kind)) throw new RecordError('body-size')
  return body
}

/**
 * Encode in canonical CBOR, checking shape but not the transition rules (see
 * validateRecord), so a partially built record can still be serialised for a
 * test or a golden.
 */
export function encodeRecord(r: CommittedRecord): Uint8Array {
  const m: Pair[] = [
    { key: keyMagic, val: fixed(r.magic, 4, 'magic') },
    { key: keyIdentityKey, val: fixed(r.identityKey, 33, 'identityKey') },
    { key: keySeq, val: unsigned(r.seq, 'seq') },
    { key: keyKind, val: BigInt(r.kind) },
    { key: keyPrev, val: fixed(r.prev, 32, 'prev') },
    { key: keySalt, val: fixed(r.salt, 32, 'salt') },
    { key: keyWC, val: fixed(r.wc, 32, 'wc') },
    { key: keyNotBefore, val: unsigned(r.notBefore, 'notBefore') },
    { key: keyNotAfter, val: unsigned(r.notAfter, 'notAfter') },
  ]
  if (r.prevWitness !== undefined) m.push({ key: keyPrevWitness, val: fixed(r.prevWitness, 32, 'prevWitness') })
  m.push({ key: keyBody, val: checkBody(r.body ?? new CborMap(), r.kind) })
  m.push({ key: keyRefs, val: asRecordError(() => encodeRefs(r.refs)) })
  if (r.successor !== undefined) m.push({ key: keySuccessor, val: fixed(r.successor, 33, 'successor') })
  for (const p of r.unknown) {
    if (typeof p.key !== 'bigint' || p.key < 0n) throw new RecordError('field', `unknown key ${typeof p.key}`)
    if (p.key <= keySuccessor) throw new RecordError('field', `unknown key ${p.key} is defined`)
    m.push(p)
  }
  return encode(new CborMap(m))
}

/**
 * Parse canonical bytes into a record, checking every defined field's shape
 * and preserving undefined keys. It does not apply the transition rules; call
 * validateRecord for those. A CborError from non-canonical bytes propagates
 * as itself.
 */
export function decodeRecord(b: Uint8Array): CommittedRecord {
  const v = decodeValue(b)
  if (!(v instanceof CborMap)) throw new RecordError('shape')
  const seen = new Set<bigint>()
  const r: Partial<CommittedRecord> & { unknown: Pair[]; refs: Ref[] } = { unknown: [], refs: [] }
  for (const p of v.entries) {
    if (typeof p.key !== 'bigint' || p.key < 0n) throw new RecordError('shape', `key ${typeof p.key}`)
    const k = p.key
    seen.add(k)
    switch (k) {
      case keyMagic:
        r.magic = fixed(p.val, 4, 'magic')
        if (!bytesEq(r.magic, MagicV1)) throw new RecordError('magic')
        break
      case keyIdentityKey:
        r.identityKey = fixed(p.val, 33, 'identityKey')
        checkKeyPrefix(r.identityKey, 'identityKey')
        break
      case keySeq:
        r.seq = unsigned(p.val, 'seq')
        break
      case keyKind: {
        const n = unsigned(p.val, 'kind')
        if (n < BigInt(KindCreate) || n > BigInt(KindManifest)) throw new RecordError('field', `kind ${n}`)
        r.kind = Number(n) as Kind
        break
      }
      case keyPrev:
        r.prev = fixed(p.val, 32, 'prev')
        break
      case keySalt:
        r.salt = fixed(p.val, 32, 'salt')
        break
      case keyWC:
        r.wc = fixed(p.val, 32, 'wc')
        break
      case keyPrevWitness:
        r.prevWitness = fixed(p.val, 32, 'prevWitness')
        break
      case keyNotBefore:
        r.notBefore = unsigned(p.val, 'notBefore')
        break
      case keyNotAfter:
        r.notAfter = unsigned(p.val, 'notAfter')
        break
      case keyBody:
        // The bound depends on the kind, so it is applied after the loop
        // rather than here: canonical CBOR happens to order key 3 before
        // key 10, but a decoder that reads a size limit out of key order is
        // one reordering away from being wrong.
        if (!(p.val instanceof CborMap)) throw new RecordError('field', 'body')
        for (const bp of p.val.entries) {
          if (typeof bp.key !== 'string') throw new RecordError('field', `body key ${typeof bp.key}`)
        }
        r.body = p.val
        break
      case keyRefs:
        r.refs = asRecordError(() => decodeRefs(p.val))
        break
      case keySuccessor:
        r.successor = fixed(p.val, 33, 'successor')
        checkKeyPrefix(r.successor, 'successor')
        break
      default:
        r.unknown.push(p)
    }
  }
  for (const k of [keyMagic, keyIdentityKey, keySeq, keyKind, keyPrev, keySalt, keyWC, keyNotBefore, keyNotAfter, keyBody, keyRefs]) {
    if (!seen.has(k)) throw new RecordError('missing', `key ${k}`)
  }
  // Now that the kind is known.
  if (encode(r.body!).length > bodyBound(r.kind!)) throw new RecordError('body-size')
  return r as CommittedRecord
}

function bytesEq(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false
  return true
}

/**
 * The transition rules a record must satisfy on its own, without the previous
 * record: the fields a kind requires, the validity window, and a sequence
 * that starts at one. Throws RecordError.
 */
export function validateRecord(r: CommittedRecord): void {
  if (!bytesEq(r.magic, MagicV1)) throw new RecordError('magic')
  if (r.seq === 0n) throw new RecordError('field', 'seq 0')
  switch (r.kind) {
    case KindCreate:
    case KindSub:
    case KindManifest:
      if (r.seq !== 1n || !isZero(r.prev) || r.prevWitness !== undefined || r.successor !== undefined) {
        throw new RecordError('kind', `kind ${r.kind} starts a chain: seq 1, zero prev, no witness, no successor`)
      }
      break
    case KindUpdate:
    case KindRotate:
    case KindRetire:
      if (r.seq < 2n || isZero(r.prev) || r.prevWitness === undefined) {
        throw new RecordError('kind', `kind ${r.kind} needs prev and prevWitness and seq >= 2`)
      }
      if ((r.kind === KindRotate) !== (r.successor !== undefined)) {
        throw new RecordError('kind', 'successor is for rotate only')
      }
      if (r.kind === KindRetire && r.body.entries.length !== 0) {
        throw new RecordError('kind', 'retire carries an empty body')
      }
      break
    default:
      throw new RecordError('field', `kind ${String(r.kind)}`)
  }
  if (r.notBefore !== 0n && r.notAfter !== 0n && r.notBefore > r.notAfter) throw new RecordError('window')
}

/** True for the errors decodeRecord and validateRecord raise. */
export function isRecordError(err: unknown): err is RecordError | CborError {
  return err instanceof RecordError || err instanceof CborError
}
