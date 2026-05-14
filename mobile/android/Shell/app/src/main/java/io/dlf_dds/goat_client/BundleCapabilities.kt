// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 dlf-dds contributors.
package io.dlf_dds.goat_client

/**
 * Mirror of the iOS shell's BundleCapabilities — what modes can the
 * currently-imported bundle drive? The Go SDK owns the authoritative
 * bundle parse via internal/bundle/ (gomobile bridge); Kotlin only
 * reflects the boolean answer.
 *
 * Until Worker A's 76N bundle-format extension lands (`inner_mesh_setup`
 * + `mobile_cert` CBOR fields), v0.1.x bundles always report
 * [supportsInnerMesh] = false. The SDK method that surfaces these flags
 * is wired so that the moment 76N adds the fields, this surface picks
 * them up with no Kotlin-side change.
 */
data class BundleCapabilities(
    val supportsWgCp0: Boolean,
    val supportsInnerMesh: Boolean,
) {
    /** Modes the user may pick, in canonical UI order. Empty when no bundle. */
    val availableModes: List<OperatingMode>
        get() = buildList {
            if (supportsWgCp0) add(OperatingMode.WG_CP0_ONLY)
            if (supportsInnerMesh) add(OperatingMode.NETBIRD_ONLY)
            if (supportsWgCp0 && supportsInnerMesh) add(OperatingMode.COMBINED)
        }

    /** True when both layers are present and the UI must ask the user. */
    val requiresUserChoice: Boolean
        get() = availableModes.size > 1

    companion object {
        val EMPTY = BundleCapabilities(supportsWgCp0 = false, supportsInnerMesh = false)
    }
}
