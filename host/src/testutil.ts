/**
 * Shared test fixtures: the Go golden, the record goldens, and minting
 * helpers under the golden's TEST-ONLY key for the transitions the golden
 * does not carry (a bad-witness pair, a sequence skip, a rotation, a
 * retirement). The helpers that know nothing of finger (the counting host,
 * the restore rows and storage, BEEF, the minter's wallet, hex) are the
 * library's test helpers, re-exported here under the names the tests use.
 *
 * Not a test file itself: the runner's glob is `*.test.js`.
 */
import { readFileSync } from 'node:fs'
import { MerklePath, PushDrop, Transaction, UnlockingScript } from '@bsv/sdk'
import { CborMap, type Output } from '@lightwebinc/bcommon'
import { fill, fromHex, row as libraryRow, wireParent, type Minter } from '@lightwebinc/bcommon/testing'
import { LockTime } from './carrier.js'
import { KeyIDProfile, KeyIDRecord, Protocol } from './keys.js'
import { encodeRecord, MagicV1, type CommittedRecord, type Kind } from './record.js'
import { Tag } from './token.js'

export {
  atomicOf,
  beefOf,
  countingHost,
  fakeStorage,
  fill,
  fromHex,
  minter,
  sha256,
  spends,
  toHex,
  wireParent,
  type CountingHost,
  type Minter,
} from '@lightwebinc/bcommon/testing'

export interface Golden {
  privateKeyHex: string
  identityKeyHex: string
  profileLockingKeyHex: string
  recordLockingKeyHex: string
  fundingTxHex: string
  fundingBumpHex: string
  fundingRootHex: string
  fundingHeight: number
  carrier1TxHex: string
  carrier1RecordHex: string
  carrier1CHex: string
  witness1Hex: string
  token1ScriptHex: string
  token1TxHex: string
  carrier2TxHex: string
  carrier2RecordHex: string
  carrier2CHex: string
  witness2Hex: string
  token2ScriptHex: string
  token2TxHex: string
  /** Spends funding outputs 0, 1 and 3 back to a funding-shaped output: the kill switch. */
  sweepTxHex: string
  mutations: {
    carrier1MineableTxHex: string
    token1BadSigScriptHex: string
    carrier2BadWitnessTxHex: string
    carrier2BadWitnessRecordHex: string
  }
}

// dist/testutil.js -> host/ -> bfinger/: the golden and the record goldens
// live in the Go tree, and are read from there rather than copied, so the two
// SDKs are checked against ONE set of bytes.
const goldenUrl = new URL('../../testdata/golden/finger-v1.json', import.meta.url)

export function golden(): Golden {
  return JSON.parse(readFileSync(goldenUrl, 'utf8')) as Golden
}

export function recordGolden(name: string): Uint8Array {
  const raw = readFileSync(new URL(`../../internal/protocol/record/testdata/${name}.hex`, import.meta.url), 'utf8')
  return fromHex(raw.trim())
}

/** The golden's transactions, parsed, with the carriers' BEEF parents wired. */
export interface Parsed {
  g: Golden
  funding: Transaction
  carrier1: Transaction
  token1: Transaction
  carrier2: Transaction
  token2: Transaction
  sweep: Transaction
}

export function parsed(): Parsed {
  const g = golden()
  const funding = Transaction.fromHex(g.fundingTxHex)
  funding.merklePath = MerklePath.fromHex(g.fundingBumpHex)
  const carrier1 = Transaction.fromHex(g.carrier1TxHex)
  const carrier2 = Transaction.fromHex(g.carrier2TxHex)
  const token1 = Transaction.fromHex(g.token1TxHex)
  const token2 = Transaction.fromHex(g.token2TxHex)
  const sweep = Transaction.fromHex(g.sweepTxHex)
  wireParent(carrier1, funding)
  wireParent(carrier2, funding)
  wireParent(token1, funding)
  wireParent(token2, token1)
  wireParent(token2, funding)
  wireParent(sweep, funding)
  return { g, funding, carrier1, token1, carrier2, token2, sweep }
}

/** A carrier for record under m's record derivation, spending funding:vout. */
export async function mintCarrier(
  m: Minter,
  record: CommittedRecord,
  funding: { txid: string; vout: number },
  opts: { lockTime?: number; sequence?: number } = {},
): Promise<Transaction> {
  const pd = new PushDrop(m.wallet)
  const s = Array.from(encodeRecord(record))
  const lock = await pd.lock([s], Protocol, KeyIDRecord, 'anyone', true, true, 'before')
  const tx = new Transaction()
  tx.lockTime = opts.lockTime ?? LockTime
  tx.addInput({
    sourceTXID: funding.txid,
    sourceOutputIndex: funding.vout,
    sequence: opts.sequence ?? 0,
    unlockingScript: new UnlockingScript([]),
  })
  tx.addOutput({ satoshis: 1, lockingScript: lock })
  return tx
}

/** A token committing to c under m's profile derivation, spending inputs. */
export async function mintToken(
  m: Minter,
  c: number[],
  inputs: Array<{ txid: string; vout: number }>,
): Promise<Transaction> {
  const pd = new PushDrop(m.wallet)
  const lock = await pd.lock([[...Tag], [...c]], Protocol, KeyIDProfile, 'anyone', true, true, 'before')
  const tx = new Transaction()
  for (const i of inputs) {
    tx.addInput({ sourceTXID: i.txid, sourceOutputIndex: i.vout, sequence: 0xffffffff, unlockingScript: new UnlockingScript([]) })
  }
  tx.addOutput({ satoshis: 1, lockingScript: lock })
  return tx
}

export function record(p: {
  identityKey: Uint8Array
  seq: bigint
  kind: Kind
  prev?: Uint8Array
  prevWitness?: Uint8Array
  wc: Uint8Array
  body?: CborMap
  successor?: Uint8Array
  salt?: Uint8Array
}): CommittedRecord {
  return {
    magic: MagicV1,
    identityKey: p.identityKey,
    seq: p.seq,
    kind: p.kind,
    prev: p.prev ?? fill(0x00),
    salt: p.salt ?? fill(0x42),
    wc: p.wc,
    prevWitness: p.prevWitness,
    notBefore: 0n,
    notAfter: 0n,
    body: p.body ?? new CborMap([{ key: 'status', val: 'minted in a test' }]),
    refs: [],
    successor: p.successor,
    unknown: [],
  }
}

/**
 * An outputs-table row for restore, as findUTXOsForTopic returns it, in
 * tm_finger. `outputsConsumed` is what the engine fills from the previous
 * coins the topic manager retained: for a carrier, its funding outpoint;
 * `consumedBy` is who spent this output, as a fake storage's findOutput
 * would answer.
 */
export function row(
  tx: Transaction,
  outputIndex: number,
  spent = false,
  links: Partial<Pick<Output, 'outputsConsumed' | 'consumedBy'>> = {},
): Output {
  return libraryRow(tx, outputIndex, 'tm_finger', spent, links)
}
