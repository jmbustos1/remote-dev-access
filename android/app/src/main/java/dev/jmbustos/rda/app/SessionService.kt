package dev.jmbustos.rda.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.os.PowerManager

/**
 * Foreground service mientras hay una sesión con un PC. Sin él, Android corta la
 * red de la app unos segundos después de bloquear la pantalla o cambiar de app
 * (visto en la fase 2b). El wake lock parcial mantiene vivos los keepalive cada
 * 5 s, que son los que impiden que el NAT cierre la puerta. Sin sesión, no corre.
 */
class SessionService : Service() {
    private var wakeLock: PowerManager.WakeLock? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_DISCONNECT -> {
                ConnectController.disconnect() // llama a stop()
                return START_NOT_STICKY
            }
            ACTION_UPDATE -> {
                notify(intent.getStringExtra(EXTRA_TEXT) ?: "")
                return START_NOT_STICKY
            }
        }
        val pc = intent?.getStringExtra(EXTRA_PC) ?: "PC"
        val n = notification("Conectando con $pc…")
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(NOTIF_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(NOTIF_ID, n)
        }
        if (wakeLock == null) {
            wakeLock = getSystemService(PowerManager::class.java)
                .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "rda:session")
                .apply { setReferenceCounted(false); acquire(MAX_SESSION_MS) }
            LogStore.app("INFO", "foreground service iniciado", "pc=$pc wakelock=sí")
        }
        return START_NOT_STICKY
    }

    override fun onDestroy() {
        wakeLock?.let { if (it.isHeld) it.release() }
        wakeLock = null
        LogStore.app("INFO", "foreground service detenido")
        super.onDestroy()
    }

    private fun notify(text: String) =
        getSystemService(NotificationManager::class.java).notify(NOTIF_ID, notification(text))

    private fun notification(text: String): Notification {
        val nm = getSystemService(NotificationManager::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && nm.getNotificationChannel(CHANNEL) == null) {
            nm.createNotificationChannel(NotificationChannel(CHANNEL, "Sesión activa", NotificationManager.IMPORTANCE_LOW))
        }
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE,
        )
        val disconnect = PendingIntent.getService(
            this, 1, Intent(this, SessionService::class.java).setAction(ACTION_DISCONNECT), PendingIntent.FLAG_IMMUTABLE,
        )
        val b = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) Notification.Builder(this, CHANNEL)
        else @Suppress("DEPRECATION") Notification.Builder(this)
        return b.setSmallIcon(R.drawable.ic_launcher)
            .setContentTitle("RDA")
            .setContentText(text)
            .setOngoing(true)
            .setContentIntent(open)
            .addAction(Notification.Action.Builder(null, "Desconectar", disconnect).build())
            .build()
    }

    companion object {
        private const val CHANNEL = "session"
        private const val NOTIF_ID = 1
        private const val ACTION_DISCONNECT = "disconnect"
        private const val ACTION_UPDATE = "update"
        private const val EXTRA_PC = "pc"
        private const val EXTRA_TEXT = "text"
        private const val MAX_SESSION_MS = 4 * 60 * 60 * 1000L // tope de seguridad del wake lock

        fun start(context: Context, pc: String) {
            val i = Intent(context, SessionService::class.java).putExtra(EXTRA_PC, pc)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) context.startForegroundService(i)
            else context.startService(i)
        }

        fun update(context: Context, st: ConnectState) {
            val text = when (st.phase) {
                SessionPhase.Connected -> "Conectado a ${st.pc} (directo, ${st.rttMs} ms)"
                SessionPhase.Lost -> "Conexión con ${st.pc} perdida, reintentando…"
                else -> return
            }
            context.startService(
                Intent(context, SessionService::class.java).setAction(ACTION_UPDATE).putExtra(EXTRA_TEXT, text),
            )
        }

        fun stop(context: Context) = context.stopService(Intent(context, SessionService::class.java))
    }
}
