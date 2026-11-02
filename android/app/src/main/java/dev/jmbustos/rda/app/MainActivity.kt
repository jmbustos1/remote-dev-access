package dev.jmbustos.rda.app

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Build
import androidx.compose.material.icons.filled.List
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import dev.jmbustos.rda.mobile.Mobile

private enum class Tab(val label: String, val icon: ImageVector) {
    Pcs("PCs", Icons.Filled.PlayArrow),
    Tests("Pruebas", Icons.Filled.Build),
    Logs("Logs", Icons.Filled.List),
    Settings("Ajustes", Icons.Filled.Settings),
}

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()

        if (savedInstanceState == null) {
            // verbose=true: el core manda también DEBUG; la pestaña Logs decide qué mostrar.
            Mobile.setLogListener(LogStore, true)
            LogStore.app("INFO", "app iniciada", "app=${BuildConfig.VERSION_NAME} core=${Mobile.version()}")
        }
        val settings = SettingsStore(applicationContext)
        if (savedInstanceState == null) ConnectController.configure(this, settings.load())
        // Android 13+: sin este permiso la notificación de sesión activa no se ve.
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
        lifecycle.addObserver(object : androidx.lifecycle.DefaultLifecycleObserver {
            // Android puede cortar la red de la app en segundo plano: dejarlo en los
            // logs permite correlacionar errores como "sendto: operation not permitted".
            override fun onStart(owner: androidx.lifecycle.LifecycleOwner) =
                LogStore.app("INFO", "app en primer plano")
            override fun onStop(owner: androidx.lifecycle.LifecycleOwner) =
                LogStore.app("INFO", "app en segundo plano")
        })

        setContent {
            MaterialTheme(colorScheme = if (isSystemInDarkTheme()) darkColorScheme() else lightColorScheme()) {
                App(settings)
            }
        }
    }
}

@Composable
private fun App(settings: SettingsStore) {
    var tab by rememberSaveable { mutableIntStateOf(Tab.Pcs.ordinal) }
    Scaffold(
        bottomBar = {
            NavigationBar {
                Tab.entries.forEach { t ->
                    NavigationBarItem(
                        selected = tab == t.ordinal,
                        onClick = { tab = t.ordinal },
                        icon = { Icon(t.icon, contentDescription = t.label) },
                        label = { Text(t.label) },
                    )
                }
            }
        },
    ) { padding ->
        val mod = Modifier.padding(padding).fillMaxSize()
        when (Tab.entries[tab]) {
            Tab.Pcs -> PcsScreen(mod)
            Tab.Tests -> TestsScreen(mod)
            Tab.Logs -> LogsScreen(mod)
            Tab.Settings -> SettingsScreen(settings, mod)
        }
    }
}
