/**
 * The shipped file rather than the tsc tree: bundle/index.js as `npm run
 * bundle` leaves it, mounted the way the host mounts a module, admitting the
 * golden token and carrier. Every other test runs on dist/, which resolves
 * the library from node_modules, so only this one proves that the bundle
 * works with the library inlined and nothing but @bsv/sdk beside it.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { Transaction } from '@bsv/sdk'
import type { Module, ModuleHost } from '@lightwebinc/bcommon'
import { RefuseReasons } from './tm_finger.js'
import { atomicOf, beefOf, countingHost, parsed } from './testutil.js'

// dist/bundle.test.js -> host/. Imported by URL, so the type checker does not
// go looking for a file that exists only after the bundle step.
const bundleUrl = new URL('../bundle/index.js', import.meta.url)

/** The library version installed, which is the one the bundle step inlined. */
function libraryVersion(): string {
  const pkg = new URL('../node_modules/@lightwebinc/bcommon/package.json', import.meta.url)
  return (JSON.parse(readFileSync(pkg, 'utf8')) as { version: string }).version
}

async function mount(): Promise<{ host: ReturnType<typeof countingHost>; mod: Module }> {
  const { default: create } = (await import(bundleUrl.href)) as { default: (host: ModuleHost) => Module }
  const host = countingHost()
  return { host, mod: create(host) }
}

test('the shipped bundle admits the golden token and carrier, and ls_finger joins them', async () => {
  const { host, mod } = await mount()
  const tm = mod.topics?.['tm_finger']
  const ls = mod.lookups?.['ls_finger']
  if (tm === undefined || ls === undefined) throw new Error('the bundle did not mount both')
  const { g, token1, carrier1 } = parsed()

  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(token1), []), { outputsToAdmit: [0, 1], coinsToRetain: [] })
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(carrier1), []), { outputsToAdmit: [0], coinsToRetain: [] })
  assert.equal(host.count('finger_admitted_total', { kind: 'token' }), 1)
  assert.equal(host.count('finger_admitted_total', { kind: 'carrier' }), 1)
  for (const reason of RefuseReasons) assert.equal(host.count('finger_refused_total', { reason }), 0, reason)

  // A refusal code comes from the inlined carrier check, and is the label
  // value the tsc tree counts under.
  const mineable = Transaction.fromHex(g.mutations.carrier1MineableTxHex)
  assert.deepEqual(await tm.identifyAdmissibleOutputs(beefOf(mineable), []), { outputsToAdmit: [], coinsToRetain: [] })
  assert.equal(host.count('finger_refused_total', { reason: 'mineable' }), 1)

  for (const tx of [token1, carrier1]) {
    await ls.outputAdmittedByTopic({ mode: 'whole-tx', atomicBEEF: atomicOf(tx), outputIndex: 0, topic: 'tm_finger' })
  }
  const answer = await ls.lookup({ service: 'ls_finger', query: { identityKey: g.identityKeyHex } })
  assert.deepEqual(
    answer.map((o) => `${o.txid}.${o.outputIndex}`),
    [`${token1.id('hex')}.0`, `${carrier1.id('hex')}.0`],
  )
  assert.equal(host.gauges.get('finger_identities')?.(), 1)
})

test('the bundle names the library version it inlines, in its banner and once in the host log', async () => {
  const version = libraryVersion()
  const banner = readFileSync(bundleUrl, 'utf8').split('\n', 1)[0] ?? ''
  assert.ok(banner.startsWith('// '), `the first line is not the banner: ${banner}`)
  assert.ok(banner.includes(`@lightwebinc/bcommon ${version} inlined`), banner)

  const { host } = await mount()
  const lines = host.logged.filter((l) => l.msg === 'finger module built with bcommon')
  assert.deepEqual(lines, [{ msg: 'finger module built with bcommon', extra: { bcommon: version } }])
})

// The shipped file carries no source map, inline or beside it: the sources a
// map points at are not deployed, so a map would only send a reader to files
// that are not there.
test('the shipped bundle carries no source map', () => {
  assert.equal(readFileSync(bundleUrl, 'utf8').includes('sourceMappingURL'), false)
  assert.equal(existsSync(new URL('../bundle/index.js.map', import.meta.url)), false)
})

