// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 dlf-dds contributors.
package io.dlf_dds.goat_client

/**
 * The v0.2 mobile three-mode triad from ADR 0840 amendment 2026-05-13.
 *
 * Android's [android.net.VpnService] is a single-claimant system-VPN slot
 * (F-109): only one app holds [android.Manifest.permission.BIND_VPN_SERVICE]
 * at a time. The triad picks which subset of {wg-cp0 outer, netbird inner}
 * runs inside that single slot.
 *
 * Raw values are kept stable across Kotlin, Swift, and the goat-clientd
 * --mode / IPC surface so the operator sees the same string everywhere.
 */
enum class OperatingMode(val raw: String) {
    /** wg-cp0 outer tunnel only. v0.1.x baseline behaviour. */
    WG_CP0_ONLY("wg-cp0-only"),

    /**
     * netbird inner mesh only — reaches mgmt + signal + relay over the
     * Block 80 public-mTLS crutch tier using the bundle's `mobile_cert`.
     * Replaces stock netbird mobile.
     */
    NETBIRD_ONLY("netbird-only"),

    /**
     * Both tunnels in the same VpnService (path A per ADR 0840 amendment
     * 2026-05-10b). wg-cp0 carries the outer reach; the inner netbird
     * mesh layers on top inside the same Go runtime.
     */
    COMBINED("combined");

    val hasWgCp0: Boolean
        get() = this == WG_CP0_ONLY || this == COMBINED

    val hasInnerMesh: Boolean
        get() = this == NETBIRD_ONLY || this == COMBINED

    val displayName: String
        get() = when (this) {
            WG_CP0_ONLY   -> "wg-cp0 only"
            NETBIRD_ONLY  -> "Inner mesh only"
            COMBINED      -> "Combined"
        }

    val blurb: String
        get() = when (this) {
            WG_CP0_ONLY  -> "Outer silent-control-plane tunnel only. Reaches wg-cp0 peers; no inner mesh."
            NETBIRD_ONLY -> "Inner mesh only — mgmt reached over the Block 80 public-mTLS crutch. Replaces stock netbird."
            COMBINED     -> "Both tunnels in one provider. wg-cp0 outer + netbird inner active."
        }

    companion object {
        /** Default for a fresh install. v0.1.x parity. */
        val DEFAULT: OperatingMode = WG_CP0_ONLY

        fun fromRaw(raw: String?): OperatingMode? =
            raw?.let { v -> values().firstOrNull { it.raw == v } }
    }
}
