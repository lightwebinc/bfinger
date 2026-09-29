/**
 * Builds what is deployed: ONE ESM file, bundle/index.js, from src/index.ts,
 * with @lightwebinc/bcommon inlined and @bsv/sdk left as its only import, so
 * the SDK that runs is the host's own copy.
 *
 * The result is then refused, and deleted, unless every input esbuild read
 * is a file of src/ or of the library's own package (not of a package nested
 * inside either), at least one of them is the library's, and the file
 * imports nothing but @bsv/sdk (bundle-check.js). Without this a second SDK,
 * or another package's runtime code, would reach the shipped file through a
 * dependency change that no line of this repository shows, and NOTICE's
 * account of what the module contains would be false.
 */
import { readFileSync, rmSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'
import { refusals } from './bundle-check.js'

const library = '@lightwebinc/bcommon'
const sdk = '@bsv/sdk'
const outfile = 'bundle/index.js'
const root = fileURLToPath(new URL('..', import.meta.url))
const readJSON = (path) => JSON.parse(readFileSync(new URL(`../${path}`, import.meta.url), 'utf8'))

// The version of the library actually installed, which is what gets
// inlined, rather than the range package.json asks for.
const version = readJSON(`node_modules/${library}/package.json`).version
const self = readJSON('package.json')
// The bundle targets what tsc compiles to, so the tests on dist/ and the
// shipped file are built for the same language level.
const target = readJSON('tsconfig.json').compilerOptions.target.toLowerCase()

rmSync(new URL('../bundle/', import.meta.url), { recursive: true, force: true })

const result = await build({
  absWorkingDir: root,
  entryPoints: ['src/index.ts'],
  outfile,
  bundle: true,
  format: 'esm',
  platform: 'node',
  target,
  external: [sdk],
  sourcemap: false,
  define: { BCOMMON_VERSION: JSON.stringify(version) },
  banner: { js: `// ${self.name} ${self.version}: tm_finger and ls_finger, with ${library} ${version} inlined` },
  metafile: true,
  logLevel: 'warning',
})

const problems = refusals(result.metafile, outfile, library, sdk)
if (problems.length > 0) {
  rmSync(new URL(`../${outfile}`, import.meta.url), { force: true })
  for (const p of problems) console.error(`bundle: ${p}`)
  console.error(`bundle: refused; ${outfile} removed`)
  process.exit(1)
}

console.log(`${outfile}: ${result.metafile.outputs[outfile].bytes} bytes, ${library} ${version} inlined, target ${target}; inputs:`)
for (const [path, { bytes }] of Object.entries(result.metafile.inputs)) console.log(`  ${path} (${bytes} bytes)`)
