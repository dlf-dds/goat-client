package io.dlf_dds.goat_client

import android.content.Context
import io.dlf_dds.goat_client.gomobile.goatclient.PlatformFiles
import java.io.File

/**
 * Per-app filesystem paths handed to the gomobile-bound engine.
 *
 * Android sandboxing means the engine cannot create files outside the
 * app's [Context.getFilesDir]/[Context.getCacheDir]. We resolve those
 * once and hand them to Go via the [PlatformFiles] gomobile interface.
 *
 * - bundle.cbor   → ConfigurationFilePath: persistent, the imported
 *                   offline-CA bundle (ECDSA P-256-signed CBOR)
 * - state.json    → StateFilePath: engine ephemeral state
 *                   (last handshake, peer-pubkey rotation, etc.)
 * - cache/        → CacheDir: debug bundles, log spool
 */
class PlatformFilesImpl(ctx: Context) : PlatformFiles {

    private val configPath: String =
        File(ctx.filesDir, "bundle.cbor").absolutePath

    private val statePath: String =
        File(ctx.filesDir, "state.json").absolutePath

    private val cacheDirPath: String =
        ctx.cacheDir.absolutePath

    override fun configurationFilePath(): String = configPath
    override fun stateFilePath(): String = statePath
    override fun cacheDir(): String = cacheDirPath
}
