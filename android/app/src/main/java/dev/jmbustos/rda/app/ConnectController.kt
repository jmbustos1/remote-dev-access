package dev.jmbustos.rda.app

import android.content.Context
import dev.jmbustos.rda.mobile.Mobile
import dev.jmbustos.rda.mobile.PunchListener
import dev.jmbustos.rda.mobile.PunchSession
import dev.jmbustos.rda.mobile.SignalClient
import dev.jmbustos.rda.mobile.SignalListener
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import org.json.JSONArray

data class RemoteDevice(val name: String, val kind: String, val online: Boolean)

enum class SessionPhase { None, Connecting, Connected, Lost }

data class ConnectState(
    val configured: Boolean = false,
    val signal: String = "sin configurar", // connecting | connected | disconnected: …
    val devices: List<RemoteDevice> = emptyList(),
    val pc: String = "",
    val phase: SessionPhase = SessionPhase.None,
    val peer: String = "",
    val rttMs: Long = 0,
    val error: String? = null,
    val chat: List<ChatLine> = emptyList(),
)

/**
 * Señalización + sesión con un PC. Vive fuera de la UI (la sesión sobrevive al
 * cambiar de pestaña) y arranca/detiene el foreground service que mantiene la red
 * con la pantalla bloqueada.
 */
object ConnectController : SignalListener, PunchListener {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private lateinit var appContext: Context
    private var client: SignalClient? = null
    private var session: PunchSession? = null

    private val _state = MutableStateFlow(ConnectState())
    val state: StateFlow<ConnectState> = _state

    /** (Re)conecta con la señalización usando los ajustes guardados. */
    fun configure(context: Context, s: AppSettings) {
        appContext = context.applicationContext
        client?.close()
        client = null
        if (s.signalingUrl.isBlank() || s.deviceName.isBlank() || s.deviceToken.isBlank()) {
            _state.update { it.copy(configured = false, signal = "sin configurar", devices = emptyList()) }
            return
        }
        _state.update { it.copy(configured = true, signal = "connecting", error = null) }
        LogStore.app("INFO", "conectando a la señalización", "url=${s.signalingUrl} nombre=${s.deviceName}")
        client = Mobile.startSignal(s.signalingUrl, s.deviceName, s.deviceToken, this)
    }

    fun refresh() = client?.refreshDevices()

    fun connect(pc: String) {
        val c = client ?: return
        if (_state.value.phase == SessionPhase.Connecting) return
        session?.close()
        _state.update { it.copy(pc = pc, phase = SessionPhase.Connecting, error = null, chat = emptyList(), peer = "") }
        SessionService.start(appContext, pc) // red y CPU aseguradas desde el primer paquete
        scope.launch {
            runCatching { c.connect(pc, this@ConnectController) }
                .onSuccess { s ->
                    session = s
                    LogStore.app("INFO", "sesión con $pc lista")
                }
                .onFailure { e ->
                    LogStore.app("ERROR", "no se pudo conectar con $pc", "err=${e.message}")
                    _state.update { it.copy(phase = SessionPhase.None, error = e.message) }
                    SessionService.stop(appContext)
                }
        }
    }

    fun send(text: String) {
        val s = session ?: return
        scope.launch {
            runCatching { s.send(text) }
                .onSuccess { _state.update { it.copy(chat = it.chat + ChatLine(true, text)) } }
                .onFailure { e -> _state.update { it.copy(error = e.message) } }
        }
    }

    fun disconnect() {
        session?.close()
        session = null
        _state.update { it.copy(phase = SessionPhase.None, peer = "", rttMs = 0) }
        if (::appContext.isInitialized) SessionService.stop(appContext)
        LogStore.app("INFO", "sesión cerrada por el usuario")
    }

    // --- SignalListener (hilos de Go) ---

    override fun onSignalState(state: String) {
        _state.update { it.copy(signal = state) }
        if (state == "connected") refresh()
    }

    override fun onDevices(devicesJSON: String) {
        val arr = JSONArray(devicesJSON)
        val devs = (0 until arr.length()).map {
            val o = arr.getJSONObject(it)
            RemoteDevice(o.getString("name"), o.getString("kind"), o.getBoolean("online"))
        }
        _state.update { it.copy(devices = devs) }
    }

    // --- PunchListener (hilos de Go) ---

    override fun onPunchEvent(kind: String, addr: String, rttMs: Long, text: String) {
        _state.update {
            when (kind) {
                "connected" -> it.copy(phase = SessionPhase.Connected, peer = addr, rttMs = rttMs, error = null)
                "rtt" -> it.copy(rttMs = rttMs)
                "message" -> it.copy(chat = it.chat + ChatLine(false, text))
                "lost" -> it.copy(phase = SessionPhase.Lost)
                else -> it
            }
        }
        if (kind == "connected" || kind == "lost") SessionService.update(appContext, _state.value)
    }
}
