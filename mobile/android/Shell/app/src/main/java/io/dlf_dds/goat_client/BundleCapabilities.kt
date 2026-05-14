// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 dlf-dds contributors.
package io.dlf_dds.goat_client

import android.content.Context
import org.json.JSONObject

/**
 * Mirror of the iOS shell's BundleCapabilities. The Go SDK owns the
 * authoritative bundle parse via internal/bundle/ (gomobile bridge);
 * Kotlin only reflects the boolean answer.
 */
data class BundleCapabilities(
    val supportsWgCp0: Boolean,
    val supportsInnerMesh: Boolean,
    val hasMobileCert: Boolean,
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
        val EMPTY = BundleCapabilities(
            supportsWgCp0 = false,
            supportsInnerMesh = false,
            hasMobileCert = false,
        )

        /**
         * Read capabilities from the persisted bundle via the gomobile
         * SDK. The Go side parses with internal/bundle.Unmarshal and
         * answers HasWgCp0() / HasInnerMesh() / HasMobileCert(). Cheap
         * (no crypto re-verify; the bundle was verified at import time).
         * Returns EMPTY when no bundle is imported or parse fails.
         */
        fun read(ctx: Context): BundleCapabilities =
            parse(GoatClient.get(ctx.applicationContext).bundleCapabilities()) ?: EMPTY

        /** Parse the SDK JSON shape. */
        fun parse(json: String): BundleCapabilities? = try {
            val o = JSONObject(json)
            BundleCapabilities(
                supportsWgCp0 = o.optBoolean("wg_cp0", false),
                supportsInnerMesh = o.optBoolean("inner_mesh", false),
                hasMobileCert = o.optBoolean("has_mobile_cert", false),
            )
        } catch (_: Throwable) { null }
    }
}
