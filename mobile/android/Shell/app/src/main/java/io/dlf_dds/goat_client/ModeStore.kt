// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 dlf-dds contributors.
package io.dlf_dds.goat_client

import android.content.Context
import android.content.SharedPreferences

/**
 * Persists the operator-selected [OperatingMode] to an app-private
 * SharedPreferences file the [MainActivity] writes and [GoatVpnService]
 * reads. Process-shared because both Activity and Service live in the
 * default `:app` process — no need for a per-user MODE_MULTI_PROCESS
 * shim. Mirrors the iOS shell's ModeStore (App Group UserDefaults).
 */
object ModeStore {
    private const val PREFS_NAME = "io.dlf_dds.goat_client.prefs"
    private const val KEY_MODE = "operating-mode"

    fun read(ctx: Context): OperatingMode {
        val raw = prefs(ctx).getString(KEY_MODE, null)
        return OperatingMode.fromRaw(raw) ?: OperatingMode.DEFAULT
    }

    fun write(ctx: Context, mode: OperatingMode) {
        prefs(ctx).edit().putString(KEY_MODE, mode.raw).apply()
    }

    /** Reset to the default mode (used by `Clear bundle`). */
    fun reset(ctx: Context) {
        prefs(ctx).edit().remove(KEY_MODE).apply()
    }

    private fun prefs(ctx: Context): SharedPreferences =
        ctx.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
}
