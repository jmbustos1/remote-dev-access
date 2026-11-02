package dev.jmbustos.rda.app

import android.content.Context
import android.text.InputType
import android.view.KeyEvent
import android.view.inputmethod.BaseInputConnection
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import android.webkit.WebView

/**
 * WebView de la terminal con su propia conexión al teclado.
 *
 * Si el teclado le habla a xterm.js a través de Chromium, Gboard "compone"
 * palabras (sugerencias, autocorrector) y xterm.js recibe letras repetidas
 * ("seq" llegaba como "seqq"). Igual que Termux, pedimos TYPE_NULL: el teclado
 * manda teclas sueltas, sin composición, y las mandamos directo al PC.
 * xterm.js queda solo para dibujar (y para teclados físicos).
 */
class TermWebView(context: Context) : WebView(context) {
    override fun onCheckIsTextEditor() = true

    override fun onCreateInputConnection(outAttrs: EditorInfo): InputConnection {
        outAttrs.inputType = InputType.TYPE_NULL
        outAttrs.imeOptions = EditorInfo.IME_FLAG_NO_EXTRACT_UI or EditorInfo.IME_FLAG_NO_FULLSCREEN
        return object : BaseInputConnection(this, false) {
            override fun commitText(text: CharSequence, newCursorPosition: Int): Boolean {
                TerminalController.typed(text.toString())
                return true
            }

            override fun deleteSurroundingText(beforeLength: Int, afterLength: Int): Boolean {
                repeat(beforeLength.coerceAtLeast(1)) { TerminalController.sendKeys("\u007f") }
                return true
            }

            override fun sendKeyEvent(event: KeyEvent): Boolean {
                if (event.action == KeyEvent.ACTION_DOWN) TerminalController.key(event)
                return true
            }
        }
    }
}
