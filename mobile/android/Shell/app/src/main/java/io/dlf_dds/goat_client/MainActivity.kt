package io.dlf_dds.goat_client

import android.content.Intent
import android.graphics.Color
import android.graphics.drawable.GradientDrawable
import android.net.VpnService
import android.os.Bundle
import android.util.Log
import android.view.View
import android.widget.RadioButton
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import io.dlf_dds.goat_client.databinding.ActivityMainBinding
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject

/**
 * MainActivity is the user-facing entry point of the goat-client Android shell.
 *
 * Four flows live here, all wired against [GoatClient] (the Kotlin-side
 * holder for the gomobile-bound goatclient.Client and its singleton lifecycle):
 *
 *   1. Import-bundle: SAF document picker → bytes → GoatClient.importBundle().
 *      The SDK validates the ECDSA P-256 signature (post-Block-79 cutover)
 *      against the pinned offline-CA root and rejects on failure.
 *
 *   2. Prepare + start VpnService: VpnService.prepare() asks the system for the
 *      always-on-VPN consent dialog (one-time per app), then the resulting
 *      ActivityResult triggers GoatVpnService.start().
 *
 *   3. Status poll: a background coroutine polls GoatClient.tunnelStatus()
 *      every 1s and reflects state to the UI. Cheap (in-memory in Go); the
 *      streaming RPC for handshake / bytes-in-out arrives with Track A.
 *
 *   4. Mode selection (v0.2): radio group renders the modes the imported
 *      bundle supports. Selecting a mode persists via [ModeStore] and, if
 *      the tunnel is up, restarts it so the new mode takes effect (the
 *      VpnService cannot hot-swap modes — the Go engine owns the data
 *      path and re-binding mid-flight isn't safe).
 *
 * QR-code bundle import (CameraX + ZXing) is deferred to a follow-up — see
 * mobile/android/README.md "Open follow-ups".
 */
class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding

    private val statusFlow = MutableStateFlow(StatusSnapshot.unconfigured())
    private val modeFlow = MutableStateFlow(OperatingMode.DEFAULT)

    private val pickBundle = registerForActivityResult(
        ActivityResultContracts.OpenDocument()
    ) { uri ->
        if (uri == null) return@registerForActivityResult
        lifecycleScope.launch {
            val bytes = withContext(Dispatchers.IO) {
                contentResolver.openInputStream(uri)?.use { it.readBytes() }
            }
            if (bytes == null || bytes.isEmpty()) {
                Log.w(TAG, "bundle import: empty payload from $uri")
                return@launch
            }
            try {
                GoatClient.importBundle(applicationContext, bytes)
                // Clamp the stored mode to what the freshly-imported bundle
                // can actually drive — a previous netbird-capable bundle
                // may have left a Combined selection behind.
                val caps = currentBundleCapabilities()
                val current = ModeStore.read(applicationContext)
                if (!caps.availableModes.contains(current)) {
                    val fallback = caps.availableModes.firstOrNull() ?: OperatingMode.DEFAULT
                    ModeStore.write(applicationContext, fallback)
                    modeFlow.value = fallback
                }
                refreshStatus()
                renderModeControls(caps, modeFlow.value)
            } catch (t: Throwable) {
                Log.e(TAG, "bundle import failed", t)
            }
        }
    }

    private val prepareVpn = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { result ->
        if (result.resultCode == RESULT_OK) {
            startVpnService()
        } else {
            Log.w(TAG, "VpnService.prepare() denied by user")
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        modeFlow.value = ModeStore.read(applicationContext)

        binding.importBundleButton.setOnClickListener {
            // application/octet-stream covers the .cbor bundle MIME shape;
            // some pickers also surface */* — we accept both.
            pickBundle.launch(arrayOf("application/octet-stream", "application/cbor", "*/*"))
        }

        binding.connectButton.setOnClickListener {
            val state = statusFlow.value.state
            if (state == "connected" || state == "connecting") {
                stopVpnService()
            } else {
                val prepareIntent = VpnService.prepare(this)
                if (prepareIntent != null) {
                    prepareVpn.launch(prepareIntent)
                } else {
                    startVpnService()
                }
            }
        }

        binding.modeGroup.setOnCheckedChangeListener { _, checkedId ->
            val picked = when (checkedId) {
                binding.modeWgCp0.id    -> OperatingMode.WG_CP0_ONLY
                binding.modeNetbird.id  -> OperatingMode.NETBIRD_ONLY
                binding.modeCombined.id -> OperatingMode.COMBINED
                else                    -> return@setOnCheckedChangeListener
            }
            if (picked == modeFlow.value) return@setOnCheckedChangeListener
            ModeStore.write(applicationContext, picked)
            modeFlow.value = picked
            binding.modeBlurb.text = picked.blurb
            // Restart only if currently up; cold-state mode picks just persist.
            val state = statusFlow.value.state
            if (state == "connected" || state == "connecting") {
                stopVpnService()
                lifecycleScope.launch {
                    // Brief debounce so the service has a chance to release.
                    kotlinx.coroutines.delay(400)
                    val prepareIntent = VpnService.prepare(this@MainActivity)
                    if (prepareIntent != null) {
                        prepareVpn.launch(prepareIntent)
                    } else {
                        startVpnService()
                    }
                }
            }
        }

        lifecycleScope.launch {
            statusFlow.collectLatest { snap -> renderStatus(snap) }
        }
        lifecycleScope.launch {
            modeFlow.collectLatest { renderModeControls(currentBundleCapabilities(), it) }
        }

        // Initial reflection of "do we already have a bundle persisted?"
        refreshStatus()
        renderModeControls(currentBundleCapabilities(), modeFlow.value)
        // Cheap 1Hz poll while activity is in the foreground.
        lifecycleScope.launch {
            while (true) {
                kotlinx.coroutines.delay(1000)
                refreshStatus()
            }
        }
    }

    private fun startVpnService() {
        val intent = Intent(this, GoatVpnService::class.java).apply {
            action = GoatVpnService.ACTION_START
        }
        startForegroundService(intent)
    }

    private fun stopVpnService() {
        val intent = Intent(this, GoatVpnService::class.java).apply {
            action = GoatVpnService.ACTION_STOP
        }
        startService(intent)
    }

    private fun refreshStatus() {
        statusFlow.value = StatusSnapshot.parse(GoatClient.tunnelStatus(applicationContext))
    }

    /**
     * Compute what the imported bundle can drive. Until 76N exposes the
     * `inner_mesh_setup` + `mobile_cert` CBOR fields through the gomobile
     * SDK, an imported v0.1.x bundle reports wg-cp0 only — the correct
     * v0.2 baseline.
     */
    private fun currentBundleCapabilities(): BundleCapabilities {
        val haveBundle = statusFlow.value.state != "unconfigured"
        return if (haveBundle)
            BundleCapabilities(supportsWgCp0 = true, supportsInnerMesh = false)
        else
            BundleCapabilities.EMPTY
    }

    private fun renderStatus(snap: StatusSnapshot) {
        binding.statusText.text = when (snap.state) {
            "unconfigured" -> getString(R.string.status_unconfigured)
            "imported"     -> getString(R.string.status_imported)
            "connecting"   -> getString(R.string.status_connecting)
            "connected"    -> getString(R.string.status_connected)
            "disconnected" -> getString(R.string.status_disconnected)
            "error"        -> getString(R.string.status_error)
            else           -> snap.state
        }
        binding.detailText.text = listOfNotNull(
            snap.bundleSum.takeIf { it.isNotEmpty() }?.let { "bundle ${it.take(12)}…" },
            snap.reason.takeIf { it.isNotEmpty() }
        ).joinToString(" · ")

        binding.connectButton.isEnabled = snap.state != "unconfigured"
        binding.connectButton.text = when (snap.state) {
            "connected", "connecting" -> getString(R.string.disconnect)
            else                      -> getString(R.string.connect)
        }

        // Render per-tunnel cards from the aggregate state + the mode. Until
        // 76N publishes per-tunnel status the cards mirror the aggregate;
        // the structure is shaped so the moment the SDK supplies separate
        // wg-cp0 + inner-mesh state, only this fn body changes.
        val cardState = when (snap.state) {
            "connected"    -> CardState.CONNECTED
            "connecting"   -> CardState.CONNECTING
            "error"        -> CardState.ERROR
            "unconfigured" -> CardState.IDLE
            else           -> CardState.IDLE
        }
        val mode = modeFlow.value
        applyCardState(
            stateLabel = binding.wgCp0State,
            dot = binding.wgCp0Dot,
            visible = mode.hasWgCp0,
            state = if (mode.hasWgCp0) cardState else CardState.DISABLED,
        )
        applyCardState(
            stateLabel = binding.innerMeshState,
            dot = binding.innerMeshDot,
            visible = mode.hasInnerMesh,
            state = if (mode.hasInnerMesh) cardState else CardState.DISABLED,
        )
        binding.wgCp0Card.visibility = if (mode.hasWgCp0) View.VISIBLE else View.GONE
        binding.innerMeshCard.visibility = if (mode.hasInnerMesh) View.VISIBLE else View.GONE
    }

    private fun renderModeControls(caps: BundleCapabilities, current: OperatingMode) {
        val available = caps.availableModes
        when {
            available.isEmpty() -> {
                binding.modeSingle.visibility = View.VISIBLE
                binding.modeSingle.text = getString(R.string.mode_no_bundle)
                binding.modeGroup.visibility = View.GONE
                binding.modeBlurb.visibility = View.GONE
            }
            available.size == 1 -> {
                binding.modeSingle.visibility = View.VISIBLE
                binding.modeSingle.text = available[0].displayName
                binding.modeGroup.visibility = View.GONE
                binding.modeBlurb.visibility = View.VISIBLE
                binding.modeBlurb.text = available[0].blurb
            }
            else -> {
                binding.modeSingle.visibility = View.GONE
                binding.modeGroup.visibility = View.VISIBLE
                binding.modeBlurb.visibility = View.VISIBLE
                binding.modeWgCp0.isEnabled = available.contains(OperatingMode.WG_CP0_ONLY)
                binding.modeNetbird.isEnabled = available.contains(OperatingMode.NETBIRD_ONLY)
                binding.modeCombined.isEnabled = available.contains(OperatingMode.COMBINED)
                checkRadioFor(current)
                binding.modeBlurb.text = current.blurb
            }
        }
    }

    private fun checkRadioFor(mode: OperatingMode) {
        val target: RadioButton = when (mode) {
            OperatingMode.WG_CP0_ONLY  -> binding.modeWgCp0
            OperatingMode.NETBIRD_ONLY -> binding.modeNetbird
            OperatingMode.COMBINED     -> binding.modeCombined
        }
        if (!target.isChecked) {
            target.isChecked = true
        }
    }

    private enum class CardState { DISABLED, IDLE, CONNECTING, CONNECTED, ERROR }

    private fun applyCardState(
        stateLabel: android.widget.TextView,
        dot: View,
        visible: Boolean,
        state: CardState,
    ) {
        stateLabel.text = getString(
            when (state) {
                CardState.DISABLED   -> R.string.state_disabled
                CardState.IDLE       -> R.string.state_idle
                CardState.CONNECTING -> R.string.state_connecting
                CardState.CONNECTED  -> R.string.state_connected
                CardState.ERROR      -> R.string.state_error
            }
        )
        val color = when (state) {
            CardState.CONNECTED  -> Color.parseColor("#20964F")  // brand green
            CardState.CONNECTING -> Color.parseColor("#E0A030")  // amber
            CardState.ERROR      -> Color.parseColor("#D03B30")  // red
            CardState.IDLE       -> Color.parseColor("#808080")  // neutral gray
            CardState.DISABLED   -> Color.parseColor("#80B0B0B0") // muted
        }
        val drawable = GradientDrawable().apply {
            shape = GradientDrawable.OVAL
            setColor(color)
        }
        dot.background = drawable
        @Suppress("UNUSED_VARIABLE")
        val _v = visible  // visibility on the parent card is enforced by the caller
    }

    companion object {
        private const val TAG = "GoatMainActivity"
    }
}

/**
 * Decoded shape of [io.dlf_dds.goat_client.gomobile.goatclient.Client.getTunnelStatus]'s
 * JSON payload. The Go side guarantees this shape; UI tolerates missing optional fields.
 */
data class StatusSnapshot(
    val state: String,
    val reason: String,
    val since: String,
    val bundleSum: String,
    val deviceName: String,
) {
    companion object {
        fun unconfigured() = StatusSnapshot("unconfigured", "", "", "", "")
        fun parse(json: String): StatusSnapshot {
            return try {
                val o = JSONObject(json)
                StatusSnapshot(
                    state      = o.optString("state", "unconfigured"),
                    reason     = o.optString("reason", ""),
                    since      = o.optString("since", ""),
                    bundleSum  = o.optString("bundleSum", ""),
                    deviceName = o.optString("deviceName", ""),
                )
            } catch (_: Throwable) {
                unconfigured()
            }
        }
    }
}
