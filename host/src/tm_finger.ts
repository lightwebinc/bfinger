/**
 * `tm_finger` admits each object on its own validity (spec section 7): a
 * state token whose field signature verifies, the one record output of a
 * carrier whose record decodes, whose lock is the identity's record key,
 * whose signature verifies, and which cannot be mined, and every funding
 * output (PushDrop [ "bf" + 0x02 ], no signature), so that a later spend of
 * a funding output is one the engine sees and reports.
 *
 * It cannot check the token's lock derivation, because the identity key is
 * not in the token; that check, and every rule that needs the previous state,
 * is the lookup service's. It never throws on bad input: the object arrived
 * from an open plane and whatever it is, it is the sender's problem, not a
 * host fault.
 */
import { Transaction } from '@bsv/sdk'
import { decodeCarrier, type CarrierRefusal } from './carrier.js'
import type { AdmittanceInstructions, ModuleHost, TopicManager } from '@lightwebinc/bcommon'
import { decodeFunding } from './funding.js'
import { inspectToken } from './token.js'

/**
 * The refusal vocabulary. Fixed and small because it is a metric label; the
 * detail goes to the log, where an unbounded string belongs.
 */
export const RefuseReasons = ['not-pushdrop', 'bad-tag', 'bad-sig', 'bad-record', 'bad-lock', 'mineable', 'other'] as const
export type RefuseReason = (typeof RefuseReasons)[number]

/**
 * What an accepted submission was. `spend` is a transaction that admits
 * nothing but consumes outputs the topic holds (the sweep of a funding
 * tree): accepted for its inputs alone, and counted so that a sweep is
 * visible on the same series as the objects it retracts.
 */
export const AdmitKinds = ['token', 'carrier', 'funding', 'spend'] as const
export type AdmitKind = (typeof AdmitKinds)[number]

export class FingerTopicManager implements TopicManager {
  constructor(private readonly host: ModuleHost) {
    // Preset, so a counter absent until its first event does not read as
    // healthy: a lane refusing everything looks BUSIER than a healthy one,
    // and an alert needs the zero to compare against.
    for (const kind of AdmitKinds) host.metrics.preset('finger_admitted_total', { kind })
    for (const reason of RefuseReasons) host.metrics.preset('finger_refused_total', { reason })
  }

  async identifyAdmissibleOutputs(beef: number[], previousCoins: number[]): Promise<AdmittanceInstructions> {
    // Every previous coin is RETAINED in every case, even when the spending
    // object is refused: it is spent on chain regardless, and keeping it is
    // what lets a formula's `history` walk the chain of tokens, and what
    // keeps a carrier's funding output in storage, marked spent by the
    // carrier, so that a second spend of it (the kill switch) is one the
    // engine reports. Dropping it would delete a fact because a later
    // object was bad; the engine would also delete the coin's own ancestry
    // (Engine.js deleteUTXODeep, lines 1613-1653).
    const coinsToRetain = [...previousCoins]
    const refuse = (reason: RefuseReason, txid?: string): AdmittanceInstructions => {
      this.host.metrics.inc('finger_refused_total', { reason })
      this.host.log('tm_finger refused', { txid: txid ?? '?', reason })
      return { outputsToAdmit: [], coinsToRetain }
    }

    let tx: Transaction
    try {
      tx = Transaction.fromBEEF(beef)
    } catch {
      return refuse('other')
    }
    const txid = tx.id('hex')

    const admitted = new Set<number>()
    let tokens = 0
    let funding = 0
    let tokenReason: RefuseReason | undefined
    for (let i = 0; i < tx.outputs.length; i++) {
      const out = tx.outputs[i]
      if (out === undefined) continue
      const insp = inspectToken(out.lockingScript)
      if (insp.kind === 'token') {
        if (insp.token.valid) {
          admitted.add(i)
          tokens++
        } else {
          tokenReason ??= 'bad-sig'
        }
        continue
      }
      if (insp.kind === 'bad-tag') {
        tokenReason ??= 'bad-tag'
        continue
      }
      // A funding output has one field and a token three, so the shapes
      // never collide; a token's change output is a funding output too.
      if (decodeFunding(out.lockingScript) !== undefined) {
        admitted.add(i)
        funding++
      }
    }

    let carrierReason: CarrierRefusal | undefined
    let carriers = 0
    const carrier = decodeCarrier(tx)
    if (typeof carrier === 'string') {
      carrierReason = carrier
    } else {
      admitted.add(carrier.outputIndex)
      carriers = 1
    }

    if (admitted.size > 0) {
      if (tokens > 0) this.host.metrics.inc('finger_admitted_total', { kind: 'token' }, tokens)
      if (carriers > 0) this.host.metrics.inc('finger_admitted_total', { kind: 'carrier' }, carriers)
      if (funding > 0) this.host.metrics.inc('finger_admitted_total', { kind: 'funding' }, funding)
      return { outputsToAdmit: [...admitted].sort((a, b) => a - b), coinsToRetain }
    }

    // Nothing finger-shaped among the outputs, but the transaction consumes
    // outputs the topic holds: the sweep of a funding tree, or any spend of
    // a funding output to somewhere else. It is accepted for its inputs
    // alone. The engine treats a submission with previous coins as accepted
    // (Engine.js isTopicSubmissionAccepted, lines 463-468: `previousCoins.length > 0`
    // is one of the accepting conditions), marks each previous output spent
    // and notifies every lookup service's outputSpent with this txid before
    // any storage mutation (markPreviousOutputsSpent, lines 582-590, through
    // markPreviousOutputSpent and notifyOutputSpent, lines 516-581), and a
    // retained coin is kept as consumed rather than deleted
    // (classifyPreviousCoins, lines 591-612). Read in
    // @lightwebinc/overlay 2.3.1's dist/esm/src/Engine.js.
    const nothingFingerShaped = tokenReason === undefined && carrierReason === 'not-pushdrop'
    if (nothingFingerShaped && previousCoins.length > 0) {
      this.host.metrics.inc('finger_admitted_total', { kind: 'spend' })
      this.host.log('tm_finger accepted a spend of held outputs', { txid, inputs: previousCoins.length })
      return { outputsToAdmit: [], coinsToRetain }
    }

    // One refusal per object, with the most specific reason found: a
    // carrier's own reason when there was a record output, else whatever
    // the token pass saw, else "not a PushDrop at all".
    const reason: RefuseReason =
      carrierReason !== undefined && carrierReason !== 'not-pushdrop'
        ? carrierReason
        : (tokenReason ?? carrierReason ?? 'not-pushdrop')
    return refuse(reason, txid)
  }

  /** Nothing is needed: each object is admitted on its own validity. */
  async identifyNeededInputs(): Promise<Array<{ txid: string; outputIndex: number }>> {
    return []
  }

  async getDocumentation(): Promise<string> {
    return [
      '# tm_finger',
      '',
      'Admits state tokens (PushDrop [tag, C] whose field signature verifies),',
      'carriers (an unmineable transaction with one record output whose',
      'record decodes, whose lock is the identity\'s record key and whose',
      'signature verifies), and funding outputs (PushDrop [ "bf" + 0x02 ]',
      'with no signature). Each object is admitted on its own validity; the',
      'join, the chain rules and the kill switch are ls_finger\'s. A',
      'transaction that admits nothing but spends held outputs (the sweep of',
      'a funding tree) is accepted for its inputs alone.',
    ].join('\n')
  }

  async getMetaData(): Promise<{ name: string; shortDescription: string }> {
    return { name: 'tm_finger', shortDescription: 'Admits committed-record state tokens, carriers and funding outputs.' }
  }
}
