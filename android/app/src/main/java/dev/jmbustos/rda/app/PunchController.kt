package dev.jmbustos.rda.app

import dev.jmbustos.rda.mobile.Mobile
import dev.jmbustos.rda.mobile.PunchListener
import dev.jmbustos.rda.mobile.PunchSession
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

enum class PunchPhase { Idle, Opening, Waiting, Punching, Inbound, Connected, Lost, Failed }

data class ChatLine(val mine: Boolean, val text: String)

data class PunchState(
    val phase: PunchPhase = PunchPhase.Idle,
    val candidates: String = "",       // línea para copiar
    val candidatesDetail: String = "", // "tipo dirección" por línea
    val peer: String = "",
    val rttMs: Long = 0,
    val error: String? = null,
    val chat: List<ChatLine> = emptyList(),
)

/**
 * Dueño de la sesión de hole punching. Vive fuera de la UI para que la sesión
 * sobreviva al cambiar de pestaña. Los eventos llegan desde hilos de Go.
 */
object PunchController : PunchListener {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var session: PunchSession? = null

    private val _state = MutableStateFlow(PunchState())
    val state: StateFlow<PunchState> = _state

    fun open() {
        if (session != null) return
        _state.value = PunchState(phase = PunchPhase.Opening)
        scope.launch {
            runCatching { Mobile.openPunchSession(this@PunchController) }
                .onSuccess { s ->
                    session = s
                    _state.update {
                        it.copy(phase = PunchPhase.Waiting, candidates = s.candidates(), candidatesDetail = s.candidatesDetail())
                    }
                }
                .onFailure { e ->
                    _state.update { it.copy(phase = PunchPhase.Failed, error = e.message) }
                }
        }
    }

    fun connect(peer: String) {
        val s = session ?: return
        scope.launch {
            runCatching { s.connect(peer.trim()) }
                .onFailure { e -> _state.update { it.copy(error = e.message) } }
                .onSuccess { _state.update { it.copy(error = null) } }
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

    fun close() {
        session?.close()
        session = null
        _state.value = PunchState()
    }

    override fun onPunchEvent(kind: String, addr: String, rttMs: Long, text: String) {
        _state.update {
            when (kind) {
                "public" -> it.copy(candidates = text, candidatesDetail = session?.candidatesDetail() ?: it.candidatesDetail)
                "punching" -> it.copy(phase = PunchPhase.Punching, peer = addr)
                "inbound" -> it.copy(phase = PunchPhase.Inbound, peer = addr)
                "connected" -> it.copy(phase = PunchPhase.Connected, peer = addr, rttMs = rttMs, error = null)
                "rtt" -> it.copy(rttMs = rttMs)
                "message" -> it.copy(chat = it.chat + ChatLine(false, text))
                "lost" -> it.copy(phase = PunchPhase.Lost)
                "failed" -> it.copy(phase = PunchPhase.Failed, error = text)
                else -> it
            }
        }
    }
}
