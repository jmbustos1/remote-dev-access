package dev.jmbustos.rda.app

import android.annotation.SuppressLint
import android.content.Context
import android.os.Handler
import android.os.Looper
import android.util.Base64
import android.view.ViewGroup
import android.view.inputmethod.InputMethodManager
import android.webkit.JavascriptInterface
import android.webkit.WebView
import dev.jmbustos.rda.mobile.Terminal
import dev.jmbustos.rda.mobile.TerminalListener
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import java.io.ByteArrayOutputStream
import java.util.concurrent.Executors

enum class TermPhase { Idle, Opening, Open, Closed }

data class TermState(
    val visible: Boolean = false,
    val phase: TermPhase = TermPhase.Idle,
    val pc: String = "",
    val message: String = "",
    val hostFingerprint: String = "",
    val ctrl: Boolean = false,
    val fontSize: Int = 13,
)

/**
 * Terminal SSH con el PC (fase 4). El WebView con xterm.js y la sesión viven aquí,
 * fuera de la UI: al cambiar de pestaña o rotar, la pantalla sigue tal cual.
 *
 *   teclado → xterm.js → RDA.input → Terminal.write ─┐
 *                                                    ├ QUIC/P2P ⇄ agente ⇄ tmux
 *   xterm.js ← rdaWrite(base64) ← OnOutput ──────────┘
 */
object TerminalController : TerminalListener {
    private val main = Handler(Looper.getMainLooper())
    private val io = Executors.newSingleThreadExecutor() // conserva el orden de las teclas
    private lateinit var settings: SettingsStore

    private var webView: WebView? = null
    private var pageReady = false
    private var cols = 80
    private var rows = 24
    private var terminal: Terminal? = null

    private val _state = MutableStateFlow(TermState())
    val state: StateFlow<TermState> = _state

    // Salida acumulada entre dos cuadros: Claude Code redibuja mucho y una
    // llamada a evaluateJavascript por fragmento ahogaría el hilo de UI.
    private val pending = ByteArrayOutputStream()
    private var flushScheduled = false

    fun init(context: Context) {
        settings = SettingsStore(context.applicationContext)
    }

    /** Muestra la terminal de pc (la abre si no hay una abierta con ese PC). */
    fun show(pc: String) {
        val st = _state.value
        _state.update { it.copy(visible = true) }
        if (st.pc == pc && (st.phase == TermPhase.Open || st.phase == TermPhase.Opening)) return
        terminal?.close()
        terminal = null
        _state.update { it.copy(pc = pc, phase = TermPhase.Opening, message = "", hostFingerprint = "") }
        if (pageReady) {
            main.post { webView?.evaluateJavascript("rdaReset()", null) }
            open()
        }
        // Si la página aún carga, open() corre cuando xterm.js avise que está lista.
    }

    fun hide() = _state.update { it.copy(visible = false) }

    fun close() {
        terminal?.close() // OnExit actualiza el estado
        terminal = null
        _state.update { it.copy(visible = false, phase = TermPhase.Idle) }
    }

    /** Teclas de la barra (Esc, flechas…): van directo, sin pasar por xterm.js. */
    fun sendKeys(seq: String) = send(seq.toByteArray())

    fun toggleCtrl() {
        val on = !_state.value.ctrl
        _state.update { it.copy(ctrl = on) }
        webView?.evaluateJavascript("rdaCtrl($on)", null)
    }

    fun setFont(px: Int) {
        val size = px.coerceIn(8, 24)
        _state.update { it.copy(fontSize = size) }
        webView?.evaluateJavascript("rdaFont($size)", null)
    }

    fun focus() = webView?.evaluateJavascript("rdaFocus()", null)

    fun showKeyboard() {
        val wv = webView ?: return
        wv.requestFocus()
        focus()
        wv.context.getSystemService(InputMethodManager::class.java).showSoftInput(wv, 0)
    }

    /** El WebView, creado una sola vez y reutilizado (se despega de su vista anterior). */
    @SuppressLint("SetJavaScriptEnabled")
    fun webView(context: Context): WebView {
        webView?.let { wv ->
            (wv.parent as? ViewGroup)?.removeView(wv)
            return wv
        }
        return WebView(context.applicationContext).apply {
            settings.javaScriptEnabled = true
            addJavascriptInterface(Bridge, "RDA")
            setBackgroundColor(0xFF1E1E1E.toInt())
            loadUrl("file:///android_asset/term/index.html")
            webView = this
        }
    }

    private fun open() {
        val pc = _state.value.pc
        val session = ConnectController.session()
        if (session == null) {
            fail("no hay sesión con $pc: conéctate primero")
            return
        }
        val key = settings.loadSshKey()
        if (key == null) {
            fail("falta la llave SSH: créala en Ajustes y autorízala en el PC")
            return
        }
        val known = settings.knownHostKey(pc) ?: ""
        val c = cols
        val r = rows
        io.execute {
            LogStore.app("INFO", "abriendo terminal", "pc=$pc cols=$c rows=$r")
            runCatching { session.openTerminal(key, known, c.toLong(), r.toLong(), "", this) }
                .onSuccess { t ->
                    terminal = t
                    if (known.isEmpty()) settings.rememberHostKey(pc, t.hostKey())
                    _state.update { it.copy(phase = TermPhase.Open, hostFingerprint = t.hostFingerprint(), message = "") }
                    LogStore.app("INFO", "terminal abierta", "pc=$pc huella=${t.hostFingerprint()}")
                    main.post { focus() }
                }
                .onFailure { e -> fail(e.message ?: e.toString()) }
        }
    }

    private fun fail(msg: String) {
        LogStore.app("ERROR", "no se pudo abrir la terminal", "err=$msg")
        _state.update { it.copy(phase = TermPhase.Closed, message = msg) }
    }

    private fun send(data: ByteArray) {
        val t = terminal ?: return
        io.execute { runCatching { t.write(data) } }
    }

    // --- TerminalListener (hilos de Go) ---

    override fun onOutput(data: ByteArray) {
        synchronized(pending) {
            pending.write(data)
            if (flushScheduled) return
            flushScheduled = true
        }
        main.postDelayed(::flush, 16)
    }

    private fun flush() {
        val bytes = synchronized(pending) {
            flushScheduled = false
            pending.toByteArray().also { pending.reset() }
        }
        if (bytes.isEmpty()) return
        val b64 = Base64.encodeToString(bytes, Base64.NO_WRAP)
        webView?.evaluateJavascript("rdaWrite('$b64')", null)
    }

    override fun onExit(reason: String) {
        terminal = null
        LogStore.app("INFO", "terminal cerrada", "motivo=$reason")
        _state.update {
            if (it.phase == TermPhase.Idle) it else it.copy(phase = TermPhase.Closed, message = reason)
        }
        val note = "\r\n\u001b[33m[terminal cerrada: $reason — tmux sigue vivo en el PC]\u001b[0m\r\n"
        onOutput(note.toByteArray())
    }

    /** Lo que xterm.js llama desde JavaScript (hilo del puente JS). */
    private object Bridge {
        @JavascriptInterface
        fun ready(c: Int, r: Int) {
            cols = c; rows = r
            LogStore.app("DEBUG", "xterm.js listo", "cols=$c filas=$r")
            main.post {
                pageReady = true
                webView?.evaluateJavascript("rdaFont(${_state.value.fontSize})", null)
                if (_state.value.phase == TermPhase.Opening && terminal == null) open()
            }
        }

        @JavascriptInterface
        fun input(data: String) = send(data.toByteArray())

        @JavascriptInterface
        fun resize(c: Int, r: Int, w: Int, h: Int) {
            cols = c; rows = r
            LogStore.app("DEBUG", "tamaño de terminal", "cols=$c filas=$r vista=${w}x${h}px")
            val t = terminal ?: return
            io.execute { runCatching { t.resize(c.toLong(), r.toLong()) } }
        }

        @JavascriptInterface
        fun ctrlUsed() = _state.update { it.copy(ctrl = false) }
    }
}
