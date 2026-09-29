/**
 * The record schema against the goldens the Go side pins. Those bytes were
 * encoded by an independent implementation (fxamacker/cbor, in
 * tools/mintprobe), so this decoder is checked against a second encoder, not
 * against itself, and the round trip proves this encoder writes the same
 * bytes. A byte that differs is a bug in exactly one of the two.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { CborMap, decodeValue, encode, type Value } from '@lightwebinc/bcommon'
import {
  KindCreate,
  KindRetire,
  KindSub,
  KindRotate,
  KindUpdate,
  MagicV1,
  MaxBodyBytes,
  MaxRefs,
  RecordError,
  decodeRecord,
  encodeRecord,
  validateRecord,
  type CommittedRecord,
  type Kind,
  KindManifest,
  MaxSubBodyBytes,
  bodyBound,
} from './record.js'
import { fill, recordGolden, toHex } from './testutil.js'

function identity(): Uint8Array {
  const k = fill(0x11, 33)
  k[0] = 0x02
  return k
}

function createRecord(): CommittedRecord {
  return {
    magic: MagicV1,
    identityKey: identity(),
    seq: 1n,
    kind: KindCreate,
    prev: fill(0x00),
    salt: fill(0x22),
    wc: fill(0x33),
    notBefore: 1700000000n,
    notAfter: 0n,
    body: new CborMap([
      { key: 'status', val: 'available' },
      { key: 'plan', val: 'pro' },
    ]),
    refs: [],
    unknown: [],
  }
}

function code(err: unknown): string | undefined {
  return err instanceof RecordError ? err.code : undefined
}

test('the create golden encodes and decodes byte for byte', () => {
  const want = recordGolden('record-v1-create')
  const got = encodeRecord(createRecord())
  assert.equal(toHex(got), toHex(want), 'encode differs from the independent encoder')
  const r = decodeRecord(want)
  assert.equal(r.seq, 1n)
  assert.equal(r.kind, KindCreate)
  assert.equal(toHex(r.identityKey), toHex(identity()))
  assert.equal(toHex(r.salt), toHex(fill(0x22)))
  assert.equal(toHex(r.wc), toHex(fill(0x33)))
  assert.equal(r.notBefore, 1700000000n)
  assert.equal(r.notAfter, 0n)
  assert.equal(r.prevWitness, undefined)
  assert.equal(r.successor, undefined)
  assert.equal(r.refs.length, 0)
  assert.equal(r.unknown.length, 0)
  assert.equal(r.body.get('status'), 'available')
  assert.doesNotThrow(() => validateRecord(r))
  assert.equal(toHex(encodeRecord(r)), toHex(want), 're-encode differs')
})

test('the update golden preserves an unknown key through a re-encode', () => {
  const want = recordGolden('record-v1-update')
  const r = decodeRecord(want)
  assert.equal(r.kind, KindUpdate)
  assert.equal(r.seq, 2n)
  assert.equal(toHex(r.prev), toHex(fill(0x66)))
  assert.ok(r.prevWitness !== undefined)
  assert.equal(toHex(r.prevWitness), toHex(fill(0x55)))
  assert.equal(r.notAfter, 1800000000n)
  assert.equal(r.refs.length, 1)
  assert.equal(r.refs[0]?.name, 'links')
  assert.equal(toHex(r.refs[0]?.root ?? new Uint8Array()), toHex(fill(0x44)))
  assert.equal(r.refs[0]?.count, 3n)
  assert.equal(r.unknown.length, 1, 'unknown keys not preserved')
  assert.equal(r.unknown[0]?.key, 99n)
  assert.doesNotThrow(() => validateRecord(r))
  assert.equal(toHex(encodeRecord(r)), toHex(want), 're-encode with the unknown key differs')
})

test('a missing required field is refused', () => {
  // Drop the body (key 10) from an otherwise valid record.
  const b = encodeRecord(createRecord())
  const m = decodeValue(b)
  assert.ok(m instanceof CborMap)
  const without = new CborMap(m.entries.filter((p) => p.key !== 10n))
  assert.throws(() => decodeRecord(encode(without)), (e: unknown) => code(e) === 'missing')
})

test('the body bound is enforced on both sides', () => {
  const r = createRecord()
  r.body = new CborMap([{ key: 'big', val: 'x'.repeat(MaxBodyBytes) }])
  assert.throws(() => encodeRecord(r), (e: unknown) => code(e) === 'body-size')
  // And on decode: a record whose body was encoded oversize by some other
  // encoder is refused here too.
  const oversized = new CborMap([
    ...decodeRecord(encodeRecord(createRecord())).unknown, // none, keeps the shape explicit
    { key: 0n, val: MagicV1 },
    { key: 1n, val: identity() },
    { key: 2n, val: 1n },
    { key: 3n, val: 1n },
    { key: 4n, val: fill(0x00) },
    { key: 5n, val: fill(0x22) },
    { key: 6n, val: fill(0x33) },
    { key: 8n, val: 0n },
    { key: 9n, val: 0n },
    { key: 10n, val: new CborMap([{ key: 'big', val: 'x'.repeat(MaxBodyBytes) }]) },
    { key: 11n, val: [] },
  ])
  assert.throws(() => decodeRecord(encode(oversized)), (e: unknown) => code(e) === 'body-size')
})

test('shape refusals', () => {
  const r = createRecord()
  r.magic = Uint8Array.from([0x62, 0x66, 0x72, 0x02])
  assert.throws(() => decodeRecord(encodeRecord(r)), (e: unknown) => code(e) === 'magic', 'unknown magic')
  assert.throws(() => decodeRecord(Uint8Array.from([0x01])), (e: unknown) => code(e) === 'shape', 'not a map')
  assert.throws(
    () => decodeRecord(encode(new CborMap([{ key: 'text', val: 1n }]))),
    (e: unknown) => code(e) === 'shape',
    'a text key',
  )
  const intKey = createRecord()
  intKey.body = new CborMap([{ key: 1n, val: 'int key' }])
  assert.throws(() => encodeRecord(intKey), (e: unknown) => code(e) === 'field', 'non-text body key')
  const collide = createRecord()
  collide.unknown = [{ key: 3n, val: 'collides with kind' }]
  assert.throws(() => encodeRecord(collide), (e: unknown) => code(e) === 'field', 'unknown key colliding with a defined key')
  const badPrefix = createRecord()
  badPrefix.identityKey = fill(0x04, 33)
  assert.throws(() => decodeRecord(encodeRecord(badPrefix)), (e: unknown) => code(e) === 'field', 'identity key prefix')
  const wrongLen = createRecord()
  wrongLen.prev = fill(0x00, 31)
  assert.throws(() => encodeRecord(wrongLen), (e: unknown) => code(e) === 'field', 'a 31-byte prev')
  const badKind = new CborMap(
    (decodeValue(encodeRecord(createRecord())) as CborMap).entries.map((p) => (p.key === 3n ? { key: 3n, val: 9n } : p)),
  )
  assert.throws(() => decodeRecord(encode(badKind)), (e: unknown) => code(e) === 'field', 'kind 9')
})

test('validate applies the kind rules', () => {
  let r = createRecord()
  r.prev = fill(0x01)
  assert.throws(() => validateRecord(r), (e: unknown) => code(e) === 'kind', 'create with prev')

  r = createRecord()
  r.kind = KindUpdate
  r.seq = 2n
  r.prev = fill(0x01)
  assert.throws(() => validateRecord(r), (e: unknown) => code(e) === 'kind', 'update without witness')
  r.prevWitness = fill(0x05)
  assert.doesNotThrow(() => validateRecord(r), 'valid update')

  r.kind = KindRotate
  assert.throws(() => validateRecord(r), (e: unknown) => code(e) === 'kind', 'rotate without successor')
  r.successor = identity()
  assert.doesNotThrow(() => validateRecord(r), 'valid rotate')

  r.kind = KindRetire
  r.successor = undefined
  assert.throws(() => validateRecord(r), (e: unknown) => code(e) === 'kind', 'retire with a body')
  r.body = new CborMap()
  assert.doesNotThrow(() => validateRecord(r), 'valid retire')

  r.notBefore = 20n
  r.notAfter = 10n
  assert.throws(() => validateRecord(r), (e: unknown) => code(e) === 'window')

  const zero = createRecord()
  zero.seq = 0n
  assert.throws(() => validateRecord(zero), (e: unknown) => code(e) === 'field', 'seq 0')
})

// A store name must identify one root. refs is an ARRAY, so the CBOR map
// rules that refuse a duplicate key never reach it, and two roots under one
// name makes "fetch the links store" a question with two answers. The Go
// codec refuses this on both encode and decode; this is the mirror.
test('a duplicate ref name is refused', () => {
  const twoRoots = new CborMap([
    { key: 0n, val: MagicV1 },
    { key: 1n, val: identity() },
    { key: 2n, val: 1n },
    { key: 3n, val: 1n },
    { key: 4n, val: new Uint8Array(32) },
    { key: 5n, val: fill(0x22) },
    { key: 6n, val: fill(0x33) },
    { key: 8n, val: 1700000000n },
    { key: 9n, val: 0n },
    { key: 10n, val: new CborMap([]) },
    {
      key: 11n,
      val: [
        new CborMap([
          { key: 'name', val: 'media' },
          { key: 'root', val: fill(0xaa) },
          { key: 'count', val: 1n },
        ]),
        new CborMap([
          { key: 'name', val: 'media' },
          { key: 'root', val: fill(0xbb) },
          { key: 'count', val: 2n },
        ]),
      ],
    },
  ])
  assert.throws(
    () => decodeRecord(encode(twoRoots)),
    (e: unknown) => e instanceof RecordError && e.code === 'dup-ref',
    'two refs entries named "media" must be refused',
  )
})

// A sub-record has a create's shape and its own kind. The kind is what keeps
// a host from reading it as a second create for the identity.
test('a sub-record is create-shaped under its own kind', () => {
  const r = createRecord()
  r.kind = KindSub
  assert.doesNotThrow(() => validateRecord(r), 'valid sub-record')
  r.seq = 2n
  assert.throws(() => validateRecord(r), (e: unknown) => code(e) === 'kind', 'sub-record at seq 2')
  r.seq = 1n
  r.prevWitness = fill(0x05)
  assert.throws(() => validateRecord(r), (e: unknown) => code(e) === 'kind', 'sub-record revealing a witness')
  const enc = encodeRecord({ ...createRecord(), kind: KindSub })
  assert.equal(decodeRecord(enc).kind, KindSub)
})

// head is the one optional member of a refs entry and it round-trips; a
// fourth member that is not head, a fifth member, and a short head each
// refuse, because unknown names inside an entry would change what the root
// is a root of.
test('a ref head round-trips and the entry width is bounded', () => {
  const r = createRecord()
  r.refs = [
    { name: 'plan', root: fill(0xaa), count: 1n, head: fill(0xcc) },
    { name: 'links', root: fill(0xaa), count: 3n },
  ]
  const enc = encodeRecord(r)
  const dec = decodeRecord(enc)
  assert.equal(dec.refs.length, 2)
  assert.equal(toHex(dec.refs[0]!.head!), toHex(fill(0xcc)))
  assert.equal(dec.refs[1]!.head, undefined)
  assert.equal(toHex(encodeRecord(dec)), toHex(enc), 're-encode differs')

  const forge = (entry: CborMap): Uint8Array => {
    const m = decodeValue(enc) as CborMap
    for (const p of m.entries) if (p.key === 11n) p.val = [entry]
    return encode(m)
  }
  const base = [
    { key: 'name', val: 'plan' },
    { key: 'root', val: fill(0xaa) },
    { key: 'count', val: 1n },
  ]
  // A member this version does not define is KEPT, not refused: see the
  // forward-compatibility test below. What still refuses is a malformed
  // head, an entry past the width bound, and a non-text member key.
  const kept = decodeRecord(forge(new CborMap([...base, { key: 'note', val: 'x' }])))
  assert.equal(kept.refs[0]!.unknown?.[0]?.key, 'note')
  for (const [label, extra] of [
    ['short head', [{ key: 'head', val: fill(0xcc).subarray(0, 31) }]],
    ['numeric member key', [{ key: 7n, val: 'x' }]],
    ['too wide', 'abcdef'.split('').map((k) => ({ key: k, val: '1' }))],
  ] as const) {
    assert.throws(() => decodeRecord(forge(new CborMap([...base, ...extra]))), (e: unknown) => code(e) === 'field', label)
  }
})

// The manifest golden, encoded by fxamacker/cbor rather than by either of
// our codecs, so the nested member maps are checked against a third
// implementation. Nothing else in a record puts a map inside an array.
test('the manifest golden decodes and re-encodes byte for byte', () => {
  const want = recordGolden('record-v1-manifest')
  const r = decodeRecord(want)
  assert.equal(r.kind, KindManifest)
  assert.equal(toHex(encodeRecord(r)), toHex(want), 're-encode differs')

  const members = r.body.get('members')
  assert.ok(Array.isArray(members), 'members is an array')
  assert.equal(members.length, 2)
  assert.equal(r.body.entries.length, 1, "a manifest's body is the member list and nothing else")

  const first = members[0]
  assert.ok(first instanceof CborMap)
  assert.equal(first.entries.length, 4, 'members are fixed width')
  assert.equal(toHex(first.get('c') as Uint8Array), toHex(fill(0xa1)))
  assert.equal(first.get('name'), 'part-1')
  assert.equal(first.get('size'), 1200n)
  assert.equal(first.get('type'), 'text/plain')

  const second = members[1]
  assert.ok(second instanceof CborMap)
  assert.equal(toHex(second.get('c') as Uint8Array), toHex(fill(0xb2)))
  assert.equal(second.get('name'), '')
  assert.equal(second.get('size'), 64n)
})

// A store carries four times a record's body, and the bound follows the
// KIND, so a transition cannot borrow a store's allowance by any route.
test('the body bound follows the kind', () => {
  assert.equal(bodyBound(KindCreate), MaxBodyBytes)
  assert.equal(bodyBound(KindUpdate), MaxBodyBytes)
  assert.equal(bodyBound(KindSub), MaxSubBodyBytes)
  assert.equal(bodyBound(KindManifest), MaxSubBodyBytes)

  const big = (kind: Kind, n: number): CommittedRecord => ({
    ...createRecord(),
    kind,
    body: new CborMap([{ key: 'x', val: 'y'.repeat(n) }]),
  })
  assert.throws(() => encodeRecord(big(KindUpdate, MaxBodyBytes)), (e: unknown) => code(e) === 'body-size')
  const enc = encodeRecord(big(KindSub, MaxBodyBytes))
  assert.equal(decodeRecord(enc).kind, KindSub, 'a sub-record of a record\'s bound round-trips')
  assert.throws(() => encodeRecord(big(KindSub, MaxSubBodyBytes)), (e: unknown) => code(e) === 'body-size')

  // The DECODER applies it by kind too, so a record minted elsewhere cannot
  // carry a store's body under a transition's kind.
  const m = decodeValue(enc) as CborMap
  for (const p of m.entries) if (p.key === 3n) p.val = BigInt(KindUpdate)
  assert.throws(() => decodeRecord(encode(m)), (e: unknown) => code(e) === 'body-size')
})

// A member a later version adds to a refs entry must not cost an existing
// reader, or an existing HOST, the whole record. Adding `head` did exactly
// that: every binary built before it refused the record outright, losing the
// identity's key and profile along with the store. Entries are now
// forward-compatible, and a reader refuses the STORE instead.
test('a refs entry carrying a member from a later version round-trips', () => {
  const r = createRecord()
  r.refs = [{ name: 'plan', root: fill(0xaa), count: 1n, head: fill(0xcc), unknown: [{ key: 'salt', val: fill(0xee) }] }]
  const enc = encodeRecord(r)
  const dec = decodeRecord(enc)
  assert.equal(dec.refs.length, 1)
  assert.equal(dec.refs[0]!.unknown?.length, 1)
  assert.equal(dec.refs[0]!.unknown![0]!.key, 'salt')
  assert.equal(toHex(dec.refs[0]!.unknown![0]!.val as Uint8Array), toHex(fill(0xee)))
  assert.equal(toHex(encodeRecord(dec)), toHex(enc), 'a host relaying it must not strip what it did not understand')

  // Still bounded, and an unknown member may not shadow a defined one.
  const wide = { ...r.refs[0]!, unknown: 'abcdef'.split('').map((k) => ({ key: k, val: '1' })) }
  assert.throws(() => encodeRecord({ ...r, refs: [wide] }), (e: unknown) => code(e) === 'field')
  const shadow = { ...r.refs[0]!, unknown: [{ key: 'count', val: 9n }] }
  assert.throws(() => encodeRecord({ ...r, refs: [shadow] }), (e: unknown) => code(e) === 'field')
})

// A reader reads every store a record links, so the number of stores is the
// number of requests a record can ask for. The bound is the Go codec's, on
// both sides: a host that admitted a wider record would serve one no reader
// accepts.
test('a record links at most MaxRefs stores', () => {
  const r = createRecord()
  const ref = (i: number) => ({ name: `s${i}`, root: fill(0xaa), count: 1n })
  r.refs = Array.from({ length: MaxRefs }, (_, i) => ref(i))
  assert.equal(decodeRecord(encodeRecord(r)).refs.length, MaxRefs)
  r.refs.push(ref(MaxRefs))
  assert.throws(() => encodeRecord(r), (e: unknown) => code(e) === 'field')
  // And on decode, from bytes a writer without the bound produced.
  const wide = decodeValue(encodeRecord({ ...r, refs: r.refs.slice(0, MaxRefs) }))
  assert.ok(wide instanceof CborMap)
  const entries = wide.entries.map((p) =>
    p.key === 11n && Array.isArray(p.val) ? { key: p.key, val: [...p.val, p.val[0]!] } : p,
  )
  assert.throws(() => decodeRecord(encode(new CborMap(entries))), (e: unknown) => code(e) === 'field')
})

// The refs codec is the library's (@lightwebinc/bcommon). A finger caller
// still sees a RecordError, with the code and the text it had before the
// codec moved, on both encode and decode.
test('refs refusals keep their RecordError texts', () => {
  const text = (f: () => unknown): string => {
    try {
      f()
    } catch (e) {
      assert.ok(e instanceof RecordError, `not a RecordError: ${String(e)}`)
      return e.message
    }
    assert.fail('not refused')
  }
  const r = createRecord()
  const ref = (name: string) => ({ name, root: fill(0xaa), count: 1n })
  const enc = (refs: CommittedRecord['refs']) => () => encodeRecord({ ...r, refs })
  assert.equal(text(enc([ref('')])), 'record: field: ref name')
  assert.equal(text(enc([ref('media'), ref('media')])), 'record: dup-ref: media')
  assert.equal(text(enc([{ ...ref('a'), root: fill(0xaa).subarray(0, 31) }])), 'record: field: ref root wants 32 bytes')
  assert.equal(text(enc([{ ...ref('a'), count: -1n }])), 'record: field: ref count wants an unsigned integer')
  assert.equal(text(enc([{ ...ref('a'), head: fill(0xcc).subarray(0, 1) }])), 'record: field: ref head wants 32 bytes')
  assert.equal(text(enc([{ ...ref('a'), unknown: [{ key: 'root', val: 1n }] }])), 'record: field: root is a defined ref member')
  const wide = { ...ref('a'), unknown: 'bcdefg'.split('').map((k) => ({ key: k, val: '1' })) }
  assert.equal(text(enc([wide])), 'record: field: ref a has 9 members')
  assert.equal(text(enc(Array.from({ length: MaxRefs + 1 }, (_, i) => ref(`s${i}`)))), `record: field: ${MaxRefs + 1} refs`)

  const forge = (refs: Value): Uint8Array => {
    const m = decodeValue(encodeRecord(r)) as CborMap
    for (const p of m.entries) if (p.key === 11n) p.val = refs
    return encode(m)
  }
  const entry = (name: string) =>
    new CborMap([
      { key: 'name', val: name },
      { key: 'root', val: fill(0xaa) },
      { key: 'count', val: 1n },
    ])
  const dec = (refs: Value) => () => decodeRecord(forge(refs))
  assert.equal(text(dec(new CborMap([]))), 'record: field: refs')
  assert.equal(text(dec(['x'])), 'record: field: ref')
  assert.equal(text(dec([entry('')])), 'record: field: ref name')
  assert.equal(text(dec([entry('media'), entry('media')])), 'record: dup-ref: media')
  assert.equal(text(dec([new CborMap([...entry('a').entries, { key: 7n, val: 'x' }])])), 'record: field: ref member key bigint')
  assert.equal(text(dec(Array.from({ length: MaxRefs + 1 }, (_, i) => entry(`s${i}`)))), `record: field: ${MaxRefs + 1} refs`)
  const entryWith = (m: { root?: Value; count?: Value; head?: Value }) =>
    new CborMap([
      { key: 'name', val: 'a' },
      { key: 'root', val: m.root ?? fill(0xaa) },
      { key: 'count', val: m.count ?? 1n },
      ...(m.head === undefined ? [] : [{ key: 'head', val: m.head }]),
    ])
  assert.equal(text(dec([entryWith({ root: fill(0xaa).subarray(0, 31) })])), 'record: field: ref root wants 32 bytes')
  assert.equal(text(dec([entryWith({ count: -1n })])), 'record: field: ref count wants an unsigned integer')
  assert.equal(text(dec([entryWith({ head: fill(0xcc).subarray(0, 1) })])), 'record: field: ref head wants 32 bytes')
  assert.equal(text(dec([entryWith({ root: fill(0xaa).subarray(0, 31), count: -1n })])), 'record: field: ref root wants 32 bytes')
  assert.equal(text(dec([entryWith({ root: fill(0xaa).subarray(0, 31), head: fill(0xcc).subarray(0, 1) })])), 'record: field: ref root wants 32 bytes')
})
