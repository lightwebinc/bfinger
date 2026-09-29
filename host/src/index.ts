/**
 * The finger module: `tm_finger` and `ls_finger`, loaded into the reference
 * overlay host by path (`OVERLAY_MODULES=<dir>/index.js`).
 *
 * What is deployed is one bundle of this entry (scripts/bundle.js): finger's
 * code with @lightwebinc/bcommon inlined, importing nothing but `@bsv/sdk`
 * by bare specifier, so it must sit inside the host's tree, beside the
 * host's node_modules, to share the host's SDK copy. Nothing here touches
 * the engine package: the library's engine interfaces are the shapes the
 * engine calls through at runtime.
 */
import type { Module, ModuleHost } from '@lightwebinc/bcommon'
import { FingerLookupService } from './ls_finger.js'
import { FingerTopicManager } from './tm_finger.js'

// Replaced with the inlined library's version when the bundle is built. The
// tsc build the tests run leaves the name undeclared, and `typeof` on an
// undeclared name is 'undefined' rather than a ReferenceError, so that build
// reports the library as resolved from node_modules instead.
declare const BCOMMON_VERSION: string | undefined

/** The @lightwebinc/bcommon version this module was built with, or 'unbundled'. */
const bcommonVersion: string = typeof BCOMMON_VERSION === 'string' ? BCOMMON_VERSION : 'unbundled'

export default function create(host: ModuleHost): Module {
  // Once per mount, because a bundle carries its own copy of the library and
  // nothing else on the host can say which one: this line is how an operator
  // confirms what is deployed.
  host.log('finger module built with bcommon', { bcommon: bcommonVersion })
  const tm = new FingerTopicManager(host)
  const ls = new FingerLookupService(host)
  host.metrics.gauge('finger_pending_tokens', () => ls.pendingTokens)
  host.metrics.gauge('finger_indexed_tokens', () => ls.tokenCount)
  host.metrics.gauge('finger_indexed_carriers', () => ls.carrierCount)
  host.metrics.gauge('finger_identities', () => ls.identityCount)
  host.metrics.gauge('finger_killed_identities', () => ls.killedCount)
  return { topics: { tm_finger: tm }, lookups: { ls_finger: ls } }
}

export type { Module, ModuleHost } from '@lightwebinc/bcommon'
export { FingerTopicManager } from './tm_finger.js'
export { FingerLookupService } from './ls_finger.js'
