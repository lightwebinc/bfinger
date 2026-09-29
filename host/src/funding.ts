/**
 * The funding output: PushDrop `[ "bf" + 0x02 ]` with no signature under the
 * identity's record derivation, so the script is
 * `<33-byte key> OP_CHECKSIG <3-byte tag> OP_DROP` and is spent by the same
 * single signature a bare pay-to-public-key takes (spec section 3).
 *
 * The tag is what lets a host admit funding outputs into the topic without
 * admitting every pay-to-public-key on the plane, and admitting them is what
 * makes the kill switch visible: a funding output the engine holds is one
 * whose second spend the engine reports.
 */
import type { PublicKey, Script } from '@bsv/sdk'
import * as bcommon from '@lightwebinc/bcommon'

/** The funding output's only field: "bf" and version 2. Frozen at the first mint. */
export const FundingTag: readonly number[] = [0x62, 0x66, 0x02]

/**
 * The locking key when the script is exactly a funding output under
 * FundingTag, else undefined. The exact-shape check is the library's
 * (@lightwebinc/bcommon).
 */
export function decodeFunding(script: Script): PublicKey | undefined {
  return bcommon.decodeFunding(script, FundingTag)
}
