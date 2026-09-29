/**
 * Finger's carrier: a carrier (@lightwebinc/bcommon) whose payload is a
 * committed record, locked under the identity's record derivation. Mirrors
 * package carrier of github.com/lightwebinc/bcommon for the mechanism and
 * internal/protocol/carrier/carrier.go for finger's rules.
 *
 * What is finger's here is the codec: which PushDrop outputs are record
 * outputs (the head-byte prefilter, and a record that decodes, skipping only
 * one whose magic or shape is not finger's), the record's own rules, and the
 * lock the identity key in the record names. The refusal codes are the
 * library's, and are the `finger_refused_total{reason}` label values.
 */
import { Utils, type PublicKey, type Script, type Transaction } from '@bsv/sdk'
import * as bcommon from '@lightwebinc/bcommon'
import { recordLockingKey } from './keys.js'
import { decodeRecord, isRecordError, validateRecord, type CommittedRecord, RecordError } from './record.js'

export { LockTime, MaxSequence, commitment, mineableRefusal, type CarrierRefusal } from '@lightwebinc/bcommon'

/** A record output decoded from its locking script alone. */
export interface CarrierOutput {
  record: CommittedRecord
  /** The exact bytes pushed, which the field signature covers. */
  recordBytes: Uint8Array
  lockingKey: PublicKey
  signature: number[]
}

export type ScriptInspection = { kind: 'carrier'; out: CarrierOutput } | { kind: 'not-record' } | { kind: 'bad-record'; detail: string }

/**
 * The committed record as a carrier payload. A record is a CBOR map (major
 * type 5) of at least eleven entries: the cheap head-byte check keeps every
 * other PushDrop output out of the full decode. A map with an unknown magic
 * or the wrong shape is somebody else's payload, not a bad record.
 */
const recordCodec: bcommon.PayloadCodec<CommittedRecord> = {
  inspect(s: Uint8Array): bcommon.PayloadInspection<CommittedRecord> {
    const head = s[0]
    if (s.length < 6 || head === undefined || head >> 5 !== 5) return { kind: 'not-payload' }
    try {
      return { kind: 'payload', payload: decodeRecord(s) }
    } catch (err) {
      if (err instanceof RecordError && (err.code === 'magic' || err.code === 'shape')) return { kind: 'not-payload' }
      if (isRecordError(err)) return { kind: 'bad-payload', detail: err.message }
      throw err
    }
  },
  validate: validateRecord,
}

/** The identity's record key, from the identity key the record carries. */
function lockingKeyFor(record: CommittedRecord): PublicKey {
  return recordLockingKey(Utils.toHex(record.identityKey))
}

function toFinger(out: bcommon.CarrierOutput<CommittedRecord>): CarrierOutput {
  return { record: out.payload, recordBytes: out.payloadBytes, lockingKey: out.lockingKey, signature: out.signature }
}

function toLibrary(out: CarrierOutput): bcommon.CarrierOutput<CommittedRecord> {
  return { payload: out.record, payloadBytes: out.recordBytes, lockingKey: out.lockingKey, signature: out.signature }
}

/**
 * Decode one locking script as a record output. A script that is not a
 * two-field PushDrop, or whose first field is not a CBOR map with the known
 * magic, is simply not a record output; a record that IS one and fails to
 * decode is a bad record, which refuses the whole carrier.
 */
export function inspectCarrierScript(script: Script): ScriptInspection {
  const insp = bcommon.inspectScript(script, recordCodec)
  return insp.kind === 'carrier' ? { kind: 'carrier', out: toFinger(insp.out) } : insp
}

/** The record's own transition rules. */
export function recordRefusal(out: CarrierOutput): bcommon.CarrierRefusal | undefined {
  return bcommon.payloadRefusal(toLibrary(out), recordCodec)
}

/** The locking key must be the identity's record key. */
export function lockRefusal(out: CarrierOutput): bcommon.CarrierRefusal | undefined {
  return bcommon.lockRefusal(toLibrary(out), lockingKeyFor)
}

/** Lock's field signature over sha256(S) under the locking key. */
export function signatureRefusal(out: CarrierOutput): bcommon.CarrierRefusal | undefined {
  return bcommon.signatureRefusal(toLibrary(out))
}

export interface Carrier {
  outputIndex: number
  record: CommittedRecord
  recordBytes: Uint8Array
  lockingKey: PublicKey
  /** The commitment: the carrier's txid in hash byte order. */
  c: number[]
}

/**
 * Decode and validate a carrier transaction, or say why not: the record's
 * rules, then unmineability, then the lock, then the signature.
 *
 * Exactly one output may be a record output: with two, the commitment would
 * name two records at once. The chain rules that need the previous state
 * (prev, witness, sequence) belong to the lookup service, not here.
 */
export function decodeCarrier(tx: Transaction): Carrier | bcommon.CarrierRefusal {
  const c = bcommon.decodeCarrier(tx, recordCodec, lockingKeyFor)
  if (typeof c === 'string') return c
  return {
    outputIndex: c.outputIndex,
    record: c.payload,
    recordBytes: c.payloadBytes,
    lockingKey: c.lockingKey,
    c: c.c,
  }
}
