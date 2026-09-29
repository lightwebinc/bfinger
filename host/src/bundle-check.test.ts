/**
 * The bundle step's refusals, fed metafiles no clean build produces. The
 * smoke test cannot hold them: a bundle with a second SDK or another
 * package inlined still mounts and admits the golden token and carrier, so
 * only these say that NOTICE's account of the shipped file is enforced.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { type CheckedMetafile, refusals } from '../scripts/bundle-check.js'

const library = '@lightwebinc/bcommon'
const sdk = '@bsv/sdk'
const outfile = 'bundle/index.js'
const lib = `node_modules/${library}/`

/** A metafile of these inputs whose outfile imports these specifiers. */
function metafile(inputs: string[], imports: string[] = [sdk]): CheckedMetafile {
  return {
    inputs: Object.fromEntries(inputs.map((path) => [path, { bytes: 1, imports: [] }])),
    outputs: { [outfile]: { imports: imports.map((path) => ({ path, kind: 'import-statement', external: true })) } },
  }
}

const clean = ['src/index.ts', 'src/tm_finger.ts', `${lib}dist/index.js`, `${lib}dist/carrier.js`]
const check = (m: CheckedMetafile): string[] => refusals(m, outfile, library, sdk)

test('a bundle of src/ and the library, importing only the SDK, is kept', () => {
  assert.deepEqual(check(metafile(clean)), [])
  // The SDK imported more than once is still one specifier.
  assert.deepEqual(check(metafile(clean, [sdk, sdk])), [])
})

test('an input outside src/ and the library is refused by name', () => {
  const outside = `input outside src/ and ${lib}:`
  for (const stray of ['stray.js', 'node_modules/other/index.js', 'node_modules/@bsv/sdk/dist/esm/mod.js']) {
    assert.deepEqual(check(metafile([...clean, stray])), [`${outside} ${stray}`], stray)
  }
})

test('a package nested inside the library or src/ is refused, though its path starts with theirs', () => {
  const nested = `${lib}node_modules/x/index.js`
  assert.deepEqual(check(metafile([...clean, nested])), [`input from a package nested under ${lib}: ${nested}`])
  const underSrc = 'src/node_modules/x/index.js'
  assert.deepEqual(check(metafile([...clean, underSrc])), [`input from a package nested under src/: ${underSrc}`])
  // A nested file is not the library's, so it does not count as inlining it.
  assert.deepEqual(check(metafile(['src/index.ts', nested])), [
    `input from a package nested under ${lib}: ${nested}`,
    `nothing from ${library} was inlined`,
  ])
})

test('a bundle with nothing from the library is refused', () => {
  assert.deepEqual(check(metafile(['src/index.ts', 'src/tm_finger.ts'])), [`nothing from ${library} was inlined`])
})

test('an import other than the SDK is refused, once per specifier', () => {
  assert.deepEqual(check(metafile(clean, [sdk, 'node:fs', 'node:fs'])), [`import other than ${sdk}: node:fs`])
})

test('a metafile without the bundle is refused', () => {
  const m = metafile(clean)
  m.outputs = {}
  assert.deepEqual(check(m), [`esbuild reported no ${outfile}`])
})
