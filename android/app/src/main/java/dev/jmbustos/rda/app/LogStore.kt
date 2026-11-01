package dev.jmbustos.rda.app

import dev.jmbustos.rda.mobile.LogListener
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

data class LogEntry(
    val time: Long,
    val level: String,
    val stage: String,
    val message: String,
    val attrs: String,
)

/**
 * Buffer circular de logs en memoria. Recibe los eventos del core Go (que llegan
 * desde hilos de Go, por eso StateFlow) y los de la propia app (etapa "app").
 */
object LogStore : LogListener {
    private const val MAX = 2000
    const val STAGE_APP = "app"

    private val _entries = MutableStateFlow<List<LogEntry>>(emptyList())
    val entries: StateFlow<List<LogEntry>> = _entries

    override fun onLog(level: String, stage: String, message: String, attrs: String) =
        add(LogEntry(System.currentTimeMillis(), level, stage.ifEmpty { "core" }, message, attrs))

    fun app(level: String, message: String, attrs: String = "") =
        add(LogEntry(System.currentTimeMillis(), level, STAGE_APP, message, attrs))

    private fun add(e: LogEntry) = _entries.update { (it + e).takeLast(MAX) }

    fun clear() = _entries.update { emptyList() }

    /** Texto plano para compartir. */
    fun export(entries: List<LogEntry> = _entries.value): String {
        val fmt = SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS", Locale.US)
        return entries.joinToString("\n") {
            "${fmt.format(Date(it.time))} ${it.level.padEnd(5)} [${it.stage}] ${it.message} ${it.attrs}".trimEnd()
        }
    }
}
