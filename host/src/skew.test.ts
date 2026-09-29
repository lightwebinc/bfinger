/**
 * One tag for both languages: the TypeScript package the modules are built
 * with is the version of the Go library go.mod requires, and package.json
 * takes it from the vendored tarball of that version. The smoke test compares
 * the banner with whatever is installed, so without this a change to one
 * side alone would pass every other check.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const library = '@lightwebinc/bcommon'
const goModule = 'github.com/lightwebinc/bcommon'

// dist/skew.test.js -> host/ and the repository root.
const read = (path: string): string => readFileSync(new URL(path, import.meta.url), 'utf8')

test('the library package is the version of the Go library go.mod requires, from its vendored tarball', () => {
  const required = [...read('../../go.mod').matchAll(/^\s*(?:require\s+)?(\S+)\s+(v\S+)/gm)]
    .filter((m) => m[1] === goModule)
    .map((m) => m[2])
  assert.equal(required.length, 1, `go.mod requires ${goModule} ${required.length} times`)

  const installed = (JSON.parse(read(`../node_modules/${library}/package.json`)) as { version: string }).version
  assert.equal(required[0], `v${installed}`)

  const pkg = JSON.parse(read('../package.json')) as { dependencies: Record<string, string> }
  assert.equal(pkg.dependencies[library], `file:vendor/lightwebinc-bcommon-${installed}.tgz`)
})
