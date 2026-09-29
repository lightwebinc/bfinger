/**
 * `ls_finger` is the join and the enforcement point (spec section 7).
 *
 * The topic manager admits each object on its own validity; this service
 * pairs a token with the carrier it commits to and checks the chain: the
 * record's rules, the token's lock under the identity's profile derivation,
 * `prev` naming the current carrier, the revealed witness hashing to the
 * current commitment, the sequence advancing by one, and the new token having
 * spent the current one. A join that fails does NOT become current: the
 * previous state stays current and the failure is counted by reason.
 *
 * It is also where the kill switch lands (spec sections 3 and 9). A carrier
 * spends a funding output; a mined transaction that spends the same output
 * makes the carrier a double spend and retracts the record. The engine
 * reports every spend of an output it holds, so the service keeps
 * `funding[outpoint] -> carrier` and, on a spend of a funding outpoint by
 * any txid other than its carrier's, drops every state that carrier is part
 * of. A killed identity answers nothing afterwards, and a token or carrier
 * of it delivered late is refused rather than re-indexed.
 *
 * Arrival order is unconstrained. A token with no carrier yet is pending; a
 * carrier with no token yet waits; a transition that arrives before the one
 * it follows is retried when that one joins. The index is in memory and
 * derived from the engine's own storage, which holds every admitted output
 * durably, so `restore` rebuilds it from the unspent outputs on start and
 * re-reads each carrier's funding outpoints so a kill survives a restart.
 *
 * Commitments are keyed as hex in HASH byte order, the order the token
 * carries and `tx.hash()` returns. The engine's txid strings are the display
 * order, reversed, so a carrier's key is its txid reversed.
 */
import { Hash, LockingScript, Transaction, Utils } from '@bsv/sdk'
import { inspectCarrierScript, lockRefusal, recordRefusal, signatureRefusal } from './carrier.js'
import {
  CborMap,
  type LookupFormula,
  type LookupQuestion,
  type LookupService,
  type LookupServiceMetaData,
  type ModuleHost,
  type Output,
  type OutputAdmittedByTopic,
  type OutputSpent,
  type RestoreStorage,
} from '@lightwebinc/bcommon'
import { decodeFunding } from './funding.js'
import { profileLockingKey } from './keys.js'
import { KindCreate, KindManifest, KindRetire, KindRotate, KindSub, validateRecord, type CommittedRecord } from './record.js'
import { decodeToken } from './token.js'

/**
 * Why a join was refused. Fixed and small because it is a metric label; the
 * identity and commitment go to the log. `killed` is an object of an
 * identity the kill switch retracted, arriving after the fact.
 */
export const JoinFailures = [
  'bad-record',
  'bad-lock',
  'duplicate-create',
  'no-current',
  'retired',
  'rotated',
  'successor-taken',
  'bad-prev',
  'bad-witness',
  'bad-seq',
  'not-spent',
  'killed',
] as const
export type JoinFailure = (typeof JoinFailures)[number]

/** Why an identity was killed. One reason today; a label so a second can be told apart. */
export const KillReasons = ['funding-spent'] as const
export type KillReason = (typeof KillReasons)[number]

interface Outpoint {
  txid: string
  outputIndex: number
}

interface TokenEntry {
  outpoint: Outpoint
  lockingKeyHex: string
  cHex: string
}

interface CarrierEntry {
  outpoint: Outpoint
  cHex: string
  record: CommittedRecord
  identityKeyHex: string
  /**
   * The outputs the carrier spends. Every one is a kill switch for it: a
   * second spend of any of them makes the carrier a double spend.
   */
  funding: Outpoint[]
}

interface FundingEntry {
  /** The carrier that spends this output, once that carrier has been admitted. */
  carrierTxid?: string
  /**
   * Every txid the engine reported as spending it. The carrier's own spend is
   * reported BEFORE the carrier is admitted (Engine.submit marks previous
   * outputs spent, then admits), so the spend cannot be judged when it
   * arrives; it is kept and judged when the carrier lands.
   */
  spenders: Set<string>
}

interface IdentityState {
  /**
   * The current token. Absent only after a restore in which the token was
   * not among the unspent rows: it was spent by a transition the join
   * refused, or evicted. Live, a joined state always has one.
   */
  tokenOutpoint?: Outpoint
  /** Current carrier, as C hex. */
  carrierC: string
  /** The carrier before it, so a reader can check the witness. */
  prevCarrierC?: string
  seq: bigint
  wc: Uint8Array
  /** Retired: no further transition is accepted. */
  terminal: boolean
  /** Rotated: the chain continues under this identity key, not this one. */
  successor?: string
}

const outpointKey = (o: Outpoint): string => `${o.txid}.${o.outputIndex}`

/** Display-order txid hex to hash-order hex. */
export function reverseHex(hex: string): string {
  return Utils.toHex(Utils.toArray(hex, 'hex').reverse())
}

/** The outpoints a transaction spends, from its inputs. */
function inputsOf(tx: Transaction): Outpoint[] {
  const out: Outpoint[] = []
  for (const input of tx.inputs) {
    const txid = input.sourceTXID ?? input.sourceTransaction?.id('hex')
    if (txid === undefined) continue
    out.push({ txid, outputIndex: input.sourceOutputIndex })
  }
  return out
}

const carrierShape = /^[0-9a-f]{64}$/i

const identityKeyShape = /^0[23][0-9a-fA-F]{64}$/

export class FingerLookupService implements LookupService {
  /**
   * `whole-tx`: a carrier's kill switch is its INPUT, which a locking-script
   * payload does not carry; the atomic BEEF does. `txid` spend notifications
   * are what tell the join which token spent the current one, and what tell
   * the kill switch who spent a funding output.
   */
  readonly admissionMode = 'whole-tx' as const
  readonly spendNotificationMode = 'txid' as const

  /** C hex -> token. */
  /**
   * Tokens by commitment. A LIST per commitment, not one entry.
   *
   * A commitment is public, and a token is admitted on its own validity: the
   * topic manager cannot check whose profile key a token is locked to,
   * because the identity key is not in the token. So anyone may mint a
   * valid token over anyone's commitment. Keeping one token per commitment
   * let a stranger's token displace the real one in the window before the
   * carrier arrives, and the join then failed `bad-lock` and never
   * recovered: the real token was gone from the map and nothing re-added
   * it, so that identity stopped advancing at this host for good.
   *
   * Neither last-writer-wins nor first-writer-wins is safe, because each
   * loses to one ordering. The map holds every candidate and the join picks
   * the one locked to the identity's profile key, which is the only test
   * that distinguishes them and is exactly the test `advance` already made.
   */
  private readonly tokens = new Map<string, TokenEntry[]>()
  /** C hex (the carrier's txid in hash order) -> carrier. */
  private readonly carriers = new Map<string, CarrierEntry>()
  /** Outpoint -> the txid that spent it. Arrives before the spender's admission. */
  private readonly spentBy = new Map<string, string>()
  /** Identity key hex -> current state. */
  private readonly states = new Map<string, IdentityState>()
  /** C hex of transitions that became current. */
  private readonly joined = new Set<string>()
  /** prev C hex -> C hex of carriers naming it, for out-of-order arrival. */
  private readonly byPrev = new Map<string, Set<string>>()
  /** Outpoint -> what was indexed there, for eviction. */
  private readonly byOutpoint = new Map<string, { kind: 'token' | 'carrier' | 'funding'; cHex: string }>()
  /** Funding outpoint -> its carrier and its spenders. */
  private readonly funding = new Map<string, FundingEntry>()
  /** Carrier txid (display order) -> the identity whose chain it is in. */
  private readonly carrierIdentity = new Map<string, string>()
  /** Identities the kill switch retracted. Nothing of theirs is indexed again. */
  private readonly killed = new Set<string>()
  /** Outpoint -> spenders, for spends heard before the outpoint was known as funding. */
  private readonly earlySpends = new Map<string, Set<string>>()
  /** Profile locking key hex -> killed identity, so a late token is refused too. */
  private readonly killedProfiles = new Map<string, string>()
  /** A scheduled rebuild of the chains from the index (see rebuildChains). */
  private rebuildTimer: ReturnType<typeof setTimeout> | undefined
  /** Set while rebuildChains replays, so its refusals are not counted twice. */
  private rebuilding = false
  /** How long a burst of arrivals may run before the one rebuild it causes. */
  rebuildDelayMs = 500

  constructor(private readonly host: ModuleHost) {
    for (const reason of JoinFailures) host.metrics.preset('finger_join_failures_total', { reason })
    for (const reason of KillReasons) host.metrics.preset('finger_killed_total', { reason })
  }

  /** Tokens plus carriers indexed, for the host's gauge. */
  get size(): number {
    return this.tokenCount + this.carriers.size
  }

  get tokenCount(): number {
    let n = 0
    for (const list of this.tokens.values()) n += list.length
    return n
  }

  get carrierCount(): number {
    return this.carriers.size
  }

  get identityCount(): number {
    return this.states.size
  }

  get killedCount(): number {
    return this.killed.size
  }

  /** Funding outpoints known, admitted or named by a carrier. */
  get fundingCount(): number {
    return this.funding.size
  }

  /** Tokens whose carrier has not arrived. */
  get pendingTokens(): number {
    let n = 0
    for (const [c, list] of this.tokens) if (!this.carriers.has(c)) n += list.length
    return n
  }

  outputAdmittedByTopic(payload: OutputAdmittedByTopic): void {
    if (payload.mode !== 'whole-tx') return
    let tx: Transaction
    try {
      tx = Transaction.fromAtomicBEEF(payload.atomicBEEF)
    } catch {
      // The engine built this BEEF from a transaction it had already
      // parsed, so this is a host fault worth a line, not a throw that
      // would surface as an engine error on someone else's submission.
      this.host.log('ls_finger ignored an admission whose BEEF did not parse', { outputIndex: payload.outputIndex })
      return
    }
    const txid = tx.id('hex')
    const out = tx.outputs[payload.outputIndex]
    if (out === undefined) return
    this.noteInputs(tx)
    const script = out.lockingScript
    const outpoint = { txid, outputIndex: payload.outputIndex }

    if (decodeFunding(script) !== undefined) {
      this.indexFunding(outpoint)
      return
    }
    const token = decodeToken(script)
    if (token !== undefined) {
      // The topic manager refused an invalid signature already; checked
      // again because this is the enforcement point and the check is cheap.
      if (!token.valid) return
      const lockingKeyHex = token.lockingKey.toString()
      const cHex = Utils.toHex(token.c)
      const victim = this.killedProfiles.get(lockingKeyHex)
      if (victim !== undefined) {
        this.refuseKilled(victim, cHex)
        return
      }
      this.indexToken({ outpoint, lockingKeyHex, cHex })
      this.join(cHex, false)
      return
    }
    const entry = this.carrierFromScript(script.toBinary(), outpoint, inputsOf(tx))
    if (entry === undefined) return
    if (this.killed.has(entry.identityKeyHex)) {
      this.refuseKilled(entry.identityKeyHex, entry.cHex)
      return
    }
    this.indexCarrier(entry)
    // A carrier whose funding output was already spent by something else
    // was retracted before it arrived; it is dropped here and never joins.
    if (!this.linkFunding(entry)) return
    this.join(entry.cHex, false)
  }

  outputSpent(payload: OutputSpent): void {
    if (payload.mode !== 'txid') return
    // The engine reports every spend of a held output, including by a
    // submission the topic manager refused, and names only its txid: enough
    // for the spend link, never enough to kill (see noteInputs).
    this.noteSpend(outpointKey({ txid: payload.txid, outputIndex: payload.outputIndex }), payload.spendingTxid, false)
  }

  /**
   * Record that `k` was spent by `spendingTxid`. The carrier's own spend of
   * its funding output is the one the design expects. Any other txid is a
   * second spend of the same output: the carrier is now a double spend, and
   * the record it carried is retracted. Idempotent: a kill drops the funding
   * entry, so hearing the same spend twice kills once.
   */
  private noteSpend(k: string, spendingTxid: string, killing: boolean): void {
    this.spentBy.set(k, spendingTxid)
    if (!killing) return
    const f = this.funding.get(k)
    if (f === undefined) {
      // A spend of an outpoint not known as funding (yet): kept aside, so a
      // carrier that arrives later on it is dead on arrival (linkFunding).
      let early = this.earlySpends.get(k)
      if (early === undefined) this.earlySpends.set(k, (early = new Set()))
      early.add(spendingTxid)
      return
    }
    f.spenders.add(spendingTxid)
    if (f.carrierTxid !== undefined && f.carrierTxid !== spendingTxid) {
      this.kill(f.carrierTxid, 'funding-spent', { outpoint: k, spendingTxid })
    }
  }

  /**
   * Every input of an admitted transaction is a spend, whether or not this
   * host holds the output it spends. The engine reports spends only of
   * outputs in storage, so a host that caught up from a peer (GASP sends
   * unspent outputs, graphs in no fixed order) could hold a sweep without
   * the funding outputs it spent and never see the kill. Reading the spends
   * from the transaction itself makes the kill independent of arrival order.
   */
  private noteInputs(tx: Transaction): void {
    const txid = tx.id('hex')
    // Only a transaction that can mine is a kill. A carrier never can (its
    // lock time is 2100 and its input non-final), and neither can any
    // re-encoding of one: a third party who malleates a carrier's signature
    // gets a different txid spending the same funding output, and would
    // otherwise retract the record without the owner's key.
    const killing = isFinal(tx)
    for (const input of tx.inputs) {
      const src = input.sourceTXID ?? input.sourceTransaction?.id('hex')
      if (src === undefined) continue
      this.noteSpend(outpointKey({ txid: src, outputIndex: input.sourceOutputIndex }), txid, killing)
    }
  }

  outputEvicted(txid: string, outputIndex: number): void {
    const k = outpointKey({ txid, outputIndex })
    const ref = this.byOutpoint.get(k)
    if (ref === undefined) return
    this.byOutpoint.delete(k)
    if (ref.kind === 'funding') {
      this.funding.delete(k)
      return
    }
    if (ref.kind === 'token') {
      this.dropToken(ref.cHex, k)
      return
    }
    const carrier = this.carriers.get(ref.cHex)
    this.carriers.delete(ref.cHex)
    if (carrier !== undefined) {
      this.byPrev.get(Utils.toHex(carrier.record.prev))?.delete(ref.cHex)
      this.carrierIdentity.delete(carrier.outpoint.txid)
    }
  }

  /**
   * Decode a carrier from its locking script. The lock and signature are
   * re-verified here although the topic manager refused a carrier failing
   * either: the cost is one derivation, and the enforcement point should not
   * trust that every object reached it through the topic manager.
   */
  private carrierFromScript(script: number[], outpoint: Outpoint, funding: Outpoint[]): CarrierEntry | undefined {
    const insp = inspectCarrierScript(LockingScript.fromBinary(script))
    if (insp.kind !== 'carrier') return undefined
    const refused = recordRefusal(insp.out) ?? lockRefusal(insp.out) ?? signatureRefusal(insp.out)
    if (refused !== undefined) {
      this.host.log('ls_finger ignored a carrier the topic manager should have refused', { txid: outpoint.txid, reason: refused })
      return undefined
    }
    // Keep only what the join reads: kind, seq, prev, prevWitness, wc,
    // successor and the identity key. The body and the refs are never
    // consulted here, and the reader gets them from the carrier's own BEEF,
    // which storage holds anyway. Retaining them put a copy of every
    // record's content in the index for the life of the process, and a
    // sub-record's body may be 64 KiB.
    insp.out.record.body = new CborMap()
    insp.out.record.refs = []
    return {
      outpoint,
      cHex: reverseHex(outpoint.txid),
      record: insp.out.record,
      identityKeyHex: Utils.toHex(insp.out.record.identityKey),
      funding,
    }
  }

  /** The spenders heard for k before it was known as funding, now claimed. */
  private takeEarly(k: string): Set<string> {
    const early = this.earlySpends.get(k) ?? new Set<string>()
    this.earlySpends.delete(k)
    return early
  }

  private indexFunding(outpoint: Outpoint): void {
    const k = outpointKey(outpoint)
    if (!this.funding.has(k)) this.funding.set(k, { spenders: this.takeEarly(k) })
    this.byOutpoint.set(k, { kind: 'funding', cHex: '' })
  }

  private indexToken(t: TokenEntry): void {
    // A second token for a commitment already current is a replay; the
    // first one keeps its place.
    if (this.joined.has(t.cHex)) return
    const list = this.tokens.get(t.cHex)
    if (list === undefined) {
      this.tokens.set(t.cHex, [t])
    } else {
      const k = outpointKey(t.outpoint)
      if (list.some((e) => outpointKey(e.outpoint) === k)) return
      list.push(t)
    }
    this.byOutpoint.set(outpointKey(t.outpoint), { kind: 'token', cHex: t.cHex })
  }

  /** Forget one token by its outpoint, and the commitment's list with it if that was the last. */
  private dropToken(cHex: string, outpointK: string): void {
    const list = this.tokens.get(cHex)
    if (list === undefined) return
    const left = list.filter((e) => outpointKey(e.outpoint) !== outpointK)
    if (left.length === 0) this.tokens.delete(cHex)
    else this.tokens.set(cHex, left)
  }

  /**
   * The token for this commitment locked to this identity's profile key,
   * out of however many were minted over it. Undefined when none is, which
   * is a commitment whose real token has not arrived yet rather than a
   * commitment nobody may claim.
   */
  private tokenFor(cHex: string, identity: string): TokenEntry | undefined {
    const list = this.tokens.get(cHex)
    if (list === undefined) return undefined
    let profile: string
    try {
      profile = profileLockingKey(identity).toString()
    } catch {
      return undefined
    }
    return list.find((t) => t.lockingKeyHex === profile)
  }

  private indexCarrier(c: CarrierEntry): void {
    if (this.joined.has(c.cHex)) return
    this.carriers.set(c.cHex, c)
    this.byOutpoint.set(outpointKey(c.outpoint), { kind: 'carrier', cHex: c.cHex })
    this.carrierIdentity.set(c.outpoint.txid, c.identityKeyHex)
    const prev = Utils.toHex(c.record.prev)
    let set = this.byPrev.get(prev)
    if (set === undefined) {
      set = new Set()
      this.byPrev.set(prev, set)
    }
    set.add(c.cHex)
  }

  /**
   * Record the carrier as the spender of each output it spends, whether or
   * not that output was ever admitted here: a carrier names its own kill
   * switch. Returns false when one of them already has a spender that is
   * not this carrier, in which case the carrier was retracted before it
   * arrived and has been killed.
   *
   * An output another carrier already claims is one this carrier spends a
   * second time, so that carrier is killed first, exactly as the engine's
   * spend notification would have done had the output been in storage (it
   * arrives before the admission, so when it is in storage this branch is
   * never reached). Only the identity's own record key can spend a funding
   * output, and the engine's SPV step checks that signature before the
   * topic manager sees the carrier, so this is the owner's doing.
   */
  private linkFunding(c: CarrierEntry): boolean {
    // A second carrier on a funding output another carrier already spends is
    // a re-encoding of that carrier (a malleated signature: same record,
    // new txid) or the owner re-using an output. Neither can mine, so it
    // proves nothing: it is dropped and the first carrier stands. Killing the
    // first here would let anyone who saw a carrier retract it.
    for (const f of c.funding) {
      const held = this.funding.get(outpointKey(f))?.carrierTxid
      if (held !== undefined && held !== c.outpoint.txid) {
        this.dropCarrier(c)
        this.host.log('ls_finger dropped a second carrier on a claimed funding output', {
          c: c.cHex, funding: outpointKey(f), held,
        })
        return false
      }
    }
    let foreign: { outpoint: string; spendingTxid: string } | undefined
    for (const f of c.funding) {
      const k = outpointKey(f)
      let e = this.funding.get(k)
      if (e === undefined) {
        e = { spenders: this.takeEarly(k) }
        this.funding.set(k, e)
      }
      e.carrierTxid = c.outpoint.txid
      for (const s of e.spenders) if (s !== c.outpoint.txid) foreign ??= { outpoint: k, spendingTxid: s }
    }
    if (foreign === undefined) return true
    this.kill(c.outpoint.txid, 'funding-spent', foreign)
    return false
  }

  /**
   * Retract a carrier and every state it is part of.
   *
   * The states to drop are the identity whose chain the carrier is in (the
   * record's own identity key, kept in `carrierIdentity` so a carrier deep
   * in the chain still names its identity) and any state whose current or
   * previous carrier it is: a rotation's successor holds the rotation record
   * as its own current state until it publishes. Every carrier and token of
   * a killed identity is dropped with it: the host keeps nothing of a
   * retracted record it would have to serve. A rotation's forward pointer
   * into a killed successor is removed, because it would name a state that
   * no longer exists; the rotated key stays refused by its spent token.
   * Counted once per identity, which is what the gauge counts too.
   */
  private kill(carrierTxid: string, reason: KillReason, extra: Record<string, unknown>): void {
    const cHex = reverseHex(carrierTxid)
    const victims = new Set<string>()
    const owner = this.carrierIdentity.get(carrierTxid)
    if (owner !== undefined) victims.add(owner)
    for (const [identity, state] of this.states) {
      if (state.carrierC === cHex || state.prevCarrierC === cHex) victims.add(identity)
    }
    for (const identity of victims) {
      this.states.delete(identity)
      if (this.killed.has(identity)) continue
      this.killed.add(identity)
      try {
        this.killedProfiles.set(profileLockingKey(identity).toString(), identity)
      } catch {
        // An identity key with no derivation never had a token that
        // joined, so there is no profile lock to refuse a late token by.
      }
      this.host.metrics.inc('finger_killed_total', { reason })
    }
    for (const state of this.states.values()) {
      if (state.successor !== undefined && victims.has(state.successor)) delete state.successor
    }
    for (const carrier of [...this.carriers.values()]) {
      if (carrier.cHex === cHex || victims.has(carrier.identityKeyHex)) this.dropCarrier(carrier)
    }
    this.host.log('ls_finger killed', { carrier: carrierTxid, reason, identities: [...victims], ...extra })
  }

  /** Forget a carrier, its token, and its claim on its funding outputs. */
  private dropCarrier(carrier: CarrierEntry): void {
    this.carriers.delete(carrier.cHex)
    this.joined.delete(carrier.cHex)
    this.byOutpoint.delete(outpointKey(carrier.outpoint))
    this.byPrev.get(Utils.toHex(carrier.record.prev))?.delete(carrier.cHex)
    this.carrierIdentity.delete(carrier.outpoint.txid)
    for (const token of this.tokens.get(carrier.cHex) ?? []) {
      this.byOutpoint.delete(outpointKey(token.outpoint))
    }
    this.tokens.delete(carrier.cHex)
    for (const f of carrier.funding) {
      const k = outpointKey(f)
      if (this.funding.get(k)?.carrierTxid === carrier.outpoint.txid) this.funding.delete(k)
    }
  }

  private refuseKilled(identity: string, cHex: string): void {
    this.host.metrics.inc('finger_join_failures_total', { reason: 'killed' })
    this.host.log('ls_finger join refused', { identity, c: cHex, reason: 'killed', restoring: false })
  }

  /** The live join: both halves must be present. */
  private join(cHex: string, restoring: boolean): boolean {
    const carrier = this.carriers.get(cHex)
    if (carrier === undefined) return false
    // The carrier names the identity, so the right token among the ones
    // minted over this commitment is the one locked to that identity's
    // profile key. Anything else was somebody else's.
    const token = this.tokenFor(cHex, carrier.identityKeyHex)
    if (token === undefined) {
      // A commitment with tokens over it but none the identity's is a
      // stranger's token and is worth counting. A commitment with no token
      // at all is simply early, which is ordinary and silent.
      if ((this.tokens.get(cHex)?.length ?? 0) > 0) {
        this.host.metrics.inc('finger_join_failures_total', { reason: 'bad-lock' })
        this.host.log('ls_finger join refused', {
          identity: carrier.identityKeyHex, c: cHex, reason: 'bad-lock', restoring,
        })
      }
      return false
    }
    return this.advance(carrier, token, restoring)
  }

  /**
   * Apply one transition to its identity's state, or refuse it. Returns
   * whether it became current.
   *
   * `restoring` relaxes two things, both because a restore is fed unspent
   * outputs only. The spend link is rebuilt from spend notifications, which
   * storage does not replay, so a transition is accepted on the chain checks
   * alone (prev, witness, sequence). And a transition whose token was spent
   * by the next one has no token row at all, so it advances the chain from
   * its carrier alone, with no token outpoint; the token's lock was checked
   * live when it joined, before the engine ever marked it spent. Carriers are
   * never spent, so the chain of records is always whole in storage.
   */
  private advance(carrier: CarrierEntry, token: TokenEntry | undefined, restoring: boolean): boolean {
    const cHex = carrier.cHex
    if (this.joined.has(cHex)) return false
    // A sub-record and a manifest are store material, not transitions. They
    // stay in the index, are answered by the carrier class and are dropped
    // by a kill, and they never touch the chain. This is checked before
    // anything else because a restore replays every carrier through here by
    // sequence, ties by commitment: read as a create, either one sorting before the
    // real one would take the chain start and orphan every transition
    // behind it. Listed rather than "any kind above 4", so a later kind
    // that IS a transition is not skipped by inheriting this rule.
    if (carrier.record.kind === KindSub || carrier.record.kind === KindManifest) return false
    if (token === undefined && !restoring) return false
    const identity = carrier.identityKeyHex
    const fail = (reason: JoinFailure): false => {
      if (this.rebuilding) return false
      this.host.metrics.inc('finger_join_failures_total', { reason })
      this.host.log('ls_finger join refused', { identity, c: cHex, reason, restoring })
      // A transition whose chain is not here yet may have predecessors that
      // are here but can never join live: see rebuildChains.
      if (reason === 'no-current' && !restoring) this.scheduleRebuild()
      return false
    }
    // Carriers of a killed identity are refused at admission and dropped at
    // the kill, so this is reached only by a path that indexed one anyway;
    // the enforcement point does not rely on the front door.
    if (this.killed.has(identity)) return fail('killed')
    const record = carrier.record
    try {
      validateRecord(record)
    } catch {
      return fail('bad-record')
    }
    if (token !== undefined) {
      let profile: string
      try {
        profile = profileLockingKey(identity).toString()
      } catch {
        return fail('bad-lock')
      }
      if (profile !== token.lockingKeyHex) return fail('bad-lock')
    }

    const current = this.states.get(identity)
    let next: IdentityState
    if (record.kind === KindCreate) {
      // A second create for a known identity is refused, whatever state the
      // identity is in: a chain starts once.
      if (current !== undefined) return fail('duplicate-create')
      next = { tokenOutpoint: token?.outpoint, carrierC: cHex, seq: record.seq, wc: record.wc, terminal: false }
    } else {
      if (current === undefined) return fail('no-current')
      if (current.terminal) return fail('retired')
      if (current.successor !== undefined) return fail('rotated')
      if (Utils.toHex(record.prev) !== current.carrierC) return fail('bad-prev')
      if (record.prevWitness === undefined || Utils.toHex(Hash.sha256(Array.from(record.prevWitness))) !== Utils.toHex(current.wc)) {
        return fail('bad-witness')
      }
      if (record.seq !== current.seq + 1n) return fail('bad-seq')
      // The spend link is checked live. A current state with no token
      // outpoint can only have come from a restore whose token row was
      // missing, and then there is no outpoint to have been spent.
      if (!restoring && token !== undefined && current.tokenOutpoint !== undefined) {
        if (this.spentBy.get(outpointKey(current.tokenOutpoint)) !== token.outpoint.txid) return fail('not-spent')
      }
      next = {
        tokenOutpoint: token?.outpoint,
        carrierC: cHex,
        prevCarrierC: current.carrierC,
        seq: record.seq,
        wc: record.wc,
        terminal: record.kind === KindRetire,
      }
      if (record.kind === KindRotate && record.successor !== undefined) {
        const successor = Utils.toHex(record.successor)
        // The chain continues under the successor from this record, so the
        // successor's "current" is this rotation. A successor that already
        // has a chain of its own cannot also be handed this one, and a
        // successor the kill switch retracted is not handed one either: the
        // rotation record stands, the chain past it does not.
        if (this.states.has(successor)) return fail('successor-taken')
        if (this.killed.has(successor)) {
          this.host.log('ls_finger rotation to a killed successor stands without it', { identity, c: cHex, successor })
        } else {
          next.successor = successor
          this.states.set(successor, { ...next, successor: undefined })
        }
      }
    }
    this.states.set(identity, next)
    this.joined.add(cHex)
    // A transition that arrived before this one and names it as prev can
    // join now. Not during a restore: its sequence-ordered pass reaches every
    // carrier once, and a retry here would evaluate, and count, a refused
    // child twice.
    if (!restoring) for (const child of this.byPrev.get(cHex) ?? []) this.join(child, false)
    return true
  }

  /**
   * Rebuild every index from the unspent outputs of the topic, in a
   * deterministic order: every funding output, carrier and token is indexed
   * first; then each carrier's funding outpoints are re-read from storage
   * and a carrier whose funding output was spent by any txid other than its
   * own is killed, exactly as a live spend notification would have; then
   * the carriers left are advanced by ascending sequence of their record
   * (ties by commitment), each with its token when the token row is
   * present, so a create always precedes its updates whatever order storage
   * returned the rows in. Spent tokens are not among the rows and are not
   * needed for the chain: a record's `prev` names the previous carrier, and
   * carriers are never spent.
   *
   * A carrier's funding outpoints come from its row's `outputsConsumed`,
   * which the engine fills with the previous coins the topic manager
   * retained at admission; a carrier admitted while its funding output was
   * not in storage has none, and then there is no spend for storage to have
   * seen either. A storage failure here propagates: a restore that skipped
   * the kill check would bring retracted records back, and the host's own
   * rule is to refuse to start rather than start wrong.
   */
  async restore(outputs: Output[], storage: RestoreStorage): Promise<number> {
    if (this.rebuildTimer !== undefined) clearTimeout(this.rebuildTimer)
    this.rebuildTimer = undefined
    this.tokens.clear()
    this.carriers.clear()
    this.spentBy.clear()
    this.states.clear()
    this.joined.clear()
    this.byPrev.clear()
    this.byOutpoint.clear()
    this.funding.clear()
    this.carrierIdentity.clear()
    this.killed.clear()
    this.killedProfiles.clear()
    this.earlySpends.clear()
    let fundingRows = 0
    const carrierRows: Array<{ entry: CarrierEntry; topic: string }> = []
    for (const o of outputs) {
      if (o.spent) continue
      const outpoint = { txid: o.txid, outputIndex: o.outputIndex }
      let script: LockingScript
      try {
        script = LockingScript.fromBinary(o.outputScript)
      } catch {
        continue
      }
      if (decodeFunding(script) !== undefined) {
        this.indexFunding(outpoint)
        fundingRows++
        continue
      }
      const token = decodeToken(script)
      if (token !== undefined) {
        if (token.valid) this.indexToken({ outpoint, lockingKeyHex: token.lockingKey.toString(), cHex: Utils.toHex(token.c) })
        continue
      }
      const entry = this.carrierFromScript(o.outputScript, outpoint, o.outputsConsumed.map((c) => ({ ...c })))
      if (entry === undefined) continue
      this.indexCarrier(entry)
      this.linkFunding(entry)
      carrierRows.push({ entry, topic: o.topic })
    }
    // The spends the stored transactions themselves record, when the host
    // passes rows with their BEEF: the same order-independent kill as
    // noteInputs gives live, for a sweep whose spent outputs were never held.
    for (const o of outputs) {
      if (o.beef === undefined) continue
      try {
        this.noteInputs(Transaction.fromBEEF(o.beef, o.txid))
      } catch {
        // A row whose BEEF does not parse still restores from its script.
      }
    }
    const carrierTxids = new Set(carrierRows.map((r) => r.entry.outpoint.txid))
    const killers = new Set(outputs.filter((o) => !carrierTxids.has(o.txid)).map((o) => o.txid))
    for (const { entry, topic } of carrierRows) {
      // Dropped already by a kill earlier in this pass (same identity).
      if (!this.carriers.has(entry.cHex)) continue
      for (const f of entry.funding) {
        const stored = await storage.findOutput(f.txid, f.outputIndex, topic)
        if (stored === null || !stored.spent) continue
        // Only a consumer the topic admitted outputs of, and not a carrier:
        // storage also records spends by submissions the topic refused.
        const foreign = stored.consumedBy.find((c) => c.txid !== entry.outpoint.txid && killers.has(c.txid))
        if (foreign === undefined) continue
        this.kill(entry.outpoint.txid, 'funding-spent', { outpoint: outpointKey(f), spendingTxid: foreign.txid, restoring: true })
        break
      }
    }
    for (const carrier of this.carriersInOrder()) this.advance(carrier, this.tokenFor(carrier.cHex, carrier.identityKeyHex), true)
    return this.tokenCount + this.carriers.size + fundingRows
  }

  /** Every carrier by ascending record sequence, ties by commitment. */
  private carriersInOrder(): CarrierEntry[] {
    return [...this.carriers.values()].sort((a, b) => {
      if (a.record.seq !== b.record.seq) return a.record.seq < b.record.seq ? -1 : 1
      return a.cHex < b.cHex ? -1 : a.cHex > b.cHex ? 1 : 0
    })
  }

  private scheduleRebuild(): void {
    if (this.rebuildTimer !== undefined) clearTimeout(this.rebuildTimer)
    this.rebuildTimer = setTimeout(() => this.rebuildChains(), this.rebuildDelayMs)
    this.rebuildTimer.unref?.()
  }

  /**
   * Re-derive every identity's chain from what is already indexed, the way
   * restore does, without reading storage.
   *
   * A host that joins late (one that catches up from a peer, or is fed a
   * backlog by hand) receives unspent outputs only: every carrier, since
   * carriers are never spent, but only the CURRENT token of each identity,
   * because every earlier one was spent by its successor. Live, a transition
   * joins only with its token and only after the one before it, so the
   * create, whose token is spent, never joins, and everything after it is
   * refused `no-current` for good. Before this, only a restart repaired such
   * a host. The walk is restore's: carriers by ascending sequence, each with
   * its token when there is one, so the chain is checked by prev, witness
   * and sequence and the current state gets its token.
   *
   * It runs once after a burst of `no-current` refusals settles, and never
   * during a restore. Kills are kept: the killed sets are not touched, and
   * advance refuses a killed identity's carriers as it does live.
   */
  rebuildChains(): void {
    if (this.rebuildTimer !== undefined) clearTimeout(this.rebuildTimer)
    this.rebuildTimer = undefined
    const before = this.joined.size
    this.states.clear()
    this.joined.clear()
    // Two carriers at one sequence of one chain are conflicting transitions;
    // advance keeps whichever it meets first. Meet first the one whose token
    // is known to have spent a token (the spend notifications kept live),
    // which is the one the chain actually took, and fall back to commitment
    // order only when neither is known.
    // A token that spent another token is a transition the chain took; a
    // spend of anything else (a fee input) says nothing about the chain.
    const spenders = new Set<string>()
    for (const list of this.tokens.values()) {
      for (const t of list) {
        const by = this.spentBy.get(outpointKey(t.outpoint))
        if (by !== undefined) spenders.add(by)
      }
    }
    const taken = (c: CarrierEntry): number => {
      const t = this.tokenFor(c.cHex, c.identityKeyHex)
      return t !== undefined && spenders.has(t.outpoint.txid) ? 0 : 1
    }
    const order = [...this.carriers.values()].sort((a, b) => {
      if (a.record.seq !== b.record.seq) return a.record.seq < b.record.seq ? -1 : 1
      return taken(a) - taken(b) || (a.cHex < b.cHex ? -1 : a.cHex > b.cHex ? 1 : 0)
    })
    this.rebuilding = true
    try {
      for (const carrier of order) this.advance(carrier, this.tokenFor(carrier.cHex, carrier.identityKeyHex), true)
    } finally {
      this.rebuilding = false
    }
    this.host.log('ls_finger rebuilt its chains from the index', { carriers: this.carriers.size, joinedBefore: before, joined: this.joined.size })
  }

  /**
   * Two questions:
   *
   *   { identityKey: "<66 hex>" }                 the current token, the
   *                                               current carrier, and the
   *                                               previous carrier if any
   *   { identityKey: "<66 hex>", pending: true }  the same, plus any token
   *                                               under that identity's
   *                                               profile key whose carrier
   *                                               has not arrived
   *
   * The previous carrier is always answered (spec section 7): a
   * reader checks the witness against it. The engine hydrates each outpoint
   * as BEEF; the record is inside the carrier's. A restored state whose
   * current token was spent by a refused transition answers without the
   * token, and the reader's NO-TOKEN refusal is the right outcome for it.
   * A killed identity answers nothing, pending included.
   */
  async lookup(question: LookupQuestion): Promise<LookupFormula> {
    const q = question.query as Record<string, unknown> | undefined
    if (typeof q?.identityKey !== 'string' || !identityKeyShape.test(q.identityKey)) {
      throw new Error(
        'ls_finger: ask { identityKey: "<66 hex>" }, { identityKey, pending: true } or { identityKey, carrier: "<64 hex>" }',
      )
    }
    // An unknown member is REFUSED, never ignored.
    //
    // A question class is the set of member names, and a host that ignored an
    // extra word would let any caller mint a new class whose answer is the
    // base answer unchanged. That matters because a class is the unit a
    // charge can attach to (spec section 14.2): ignoring unknown members
    // would make every free answer available under a priceable alias.
    for (const member of Object.keys(q)) {
      if (member !== 'identityKey' && member !== 'pending' && member !== 'carrier') {
        throw new Error(`ls_finger: unknown query member ${JSON.stringify(member)}`)
      }
    }
    const identity = q.identityKey.toLowerCase()
    const out: LookupFormula = []
    if (this.killed.has(identity)) return out

    // One sub-store carrier by its commitment, scoped to the identity that
    // published it. The host asserts only that it holds this carrier under
    // this identity: it holds no root-to-member index and cannot say whether
    // the carrier belongs to any store. Membership is the reader's check
    // against the ref root, which is why this answer is useful without the
    // host being honest.
    if (q.carrier !== undefined) {
      if (typeof q.carrier !== 'string' || !carrierShape.test(q.carrier)) {
        throw new Error('ls_finger: carrier must be a 64-hex txid in display order')
      }
      const entry = this.carriers.get(reverseHex(q.carrier.toLowerCase()))
      if (entry !== undefined && entry.identityKeyHex === identity) out.push({ ...entry.outpoint })
      return out
    }

    const state = this.states.get(identity)
    if (state !== undefined) {
      if (state.tokenOutpoint !== undefined) out.push({ ...state.tokenOutpoint })
      const current = this.carriers.get(state.carrierC)
      if (current !== undefined) out.push({ ...current.outpoint })
      if (state.prevCarrierC !== undefined) {
        const prev = this.carriers.get(state.prevCarrierC)
        if (prev !== undefined) out.push({ ...prev.outpoint })
      }
    }
    if (q.pending === true) {
      let profile: string
      try {
        profile = profileLockingKey(identity).toString()
      } catch {
        return out
      }
      for (const [c, list] of this.tokens) {
        if (this.carriers.has(c)) continue
        for (const t of list) if (t.lockingKeyHex === profile) out.push({ ...t.outpoint })
      }
    }
    return out
  }

  async getDocumentation(): Promise<string> {
    return [
      '# ls_finger',
      '',
      'Answers `{ identityKey }` with the current state token, the current',
      'carrier and the previous carrier, once a token and the carrier it',
      'commits to have both arrived and the chain checks pass. With',
      '`pending: true` it also lists tokens under that identity whose carrier',
      'has not arrived. A funding output spent by anything other than its',
      'carrier kills the carrier: every state it is part of is dropped and',
      'the identity answers nothing afterwards.',
    ].join('\n')
  }

  async getMetaData(): Promise<LookupServiceMetaData> {
    return { name: 'ls_finger', shortDescription: 'Joins committed-record tokens and carriers; answers by identity key.' }
  }
}

/**
 * Whether a transaction can be mined as it stands: lock time zero, or every
 * input final. A carrier is built never to be (lock time 2100, a non-final
 * input), and so is every re-encoding of one.
 */
function isFinal(tx: Transaction): boolean {
  return tx.lockTime === 0 || tx.inputs.every((i) => (i.sequence ?? 0xffffffff) === 0xffffffff)
}
