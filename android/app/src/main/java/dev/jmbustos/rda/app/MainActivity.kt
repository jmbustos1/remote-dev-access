package dev.jmbustos.rda.app

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Box
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
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import dev.jmbustos.rda.mobile.Mobile

private enum class Tab(val label: String, val icon: ImageVector) {
    Terminal("Terminal", Icons.Filled.PlayArrow),
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

        setContent {
            MaterialTheme(colorScheme = if (isSystemInDarkTheme()) darkColorScheme() else lightColorScheme()) {
                App(settings)
            }
        }
    }
}

@Composable
private fun App(settings: SettingsStore) {
    var tab by rememberSaveable { mutableIntStateOf(Tab.Tests.ordinal) }
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
            Tab.Terminal -> Box(mod, contentAlignment = Alignment.Center) {
                Text("La terminal llega en la fase 4", style = MaterialTheme.typography.bodyLarge)
            }
            Tab.Tests -> TestsScreen(mod)
            Tab.Logs -> LogsScreen(mod)
            Tab.Settings -> SettingsScreen(settings, mod)
        }
    }
}
