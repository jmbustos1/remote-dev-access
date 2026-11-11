# Atravesar NAT: teoría y decisiones del proyecto

Notas de estudio a partir de un caso real: el 2026-11-09 cambió el router de la casa, el nuevo
no tiene UPnP, y el celular por 4G dejó de conectar con el PC. Aquí está el porqué, en términos
de paquetes, y las opciones que se evaluaron. Las decisiones quedan en [PLAN.md](../PLAN.md)
(fase 5) y las mediciones en [experimentos.md](experimentos.md).

## 1. Vocabulario: mapeo y filtrado

Un NAT hace dos cosas distintas, y conviene no mezclarlas (RFC 4787):

**Mapeo:** cuando un paquete *sale*, qué IP:puerto público le asigna.

| Tipo | Qué hace | Ejemplo (socket interno `10.0.0.5:5000`) |
|---|---|---|
| Independiente del destino (EIM) | el mismo público hacia cualquier destino | → A: `P:40001`, → B: `P:40001` |
| Dependiente de la IP destino (ADM) | cambia si cambia la IP destino | → A:3478 `P:40001`, → A:19302 `P:40001`, → B `P:40002` |
| Dependiente de IP y puerto (APDM, "simétrico") | cambia con cualquier destino nuevo | → A:3478 `P:40001`, → A:19302 `P:40002` |

**Filtrado:** cuando un paquete *entra* a un público ya mapeado, a quién deja pasar.

| Tipo | Deja entrar a |
|---|---|
| Independiente (EIF, "cono completo") | cualquiera |
| Dependiente de la IP (ADF) | IPs a las que el interno ya les escribió |
| Dependiente de IP y puerto (APDF) | solo el IP:puerto exacto al que el interno escribió |

**STUN** (RFC 5389) solo mide el mapeo hacia el servidor STUN. Si el mapeo es EIM, ese mismo
IP:puerto sirve para cualquiera. Si es simétrico, el puerto que vio STUN **no** es el que verá el
otro peer.

**CGNAT** (RFC 6888): la operadora móvil pone un NAT propio delante de miles de clientes. El
celular tiene una IP privada (`10.x`) y comparte IP pública con otros clientes.

## 2. El caso: por qué antes funcionaba y ahora no

> **Corrección (2026-11-10).** Esta sección explica la hipótesis con la que se diseñó la fase 5:
> "el 4G es simétrico". La medición con `rda natlab` la **refutó**: el CGNAT del 4G es EIM, con
> asignación por bloque (~512 puertos por cliente). Se deja la traza porque explica bien el caso
> simétrico, que sí existe en otras redes. La lección de método está en la sección 8.

Lados (hipótesis original):
- **Casa (router nuevo):** mapeo EIM que además conserva el número de puerto; filtrado estricto (APDF).
- **Celular 4G:** CGNAT con mapeo simétrico; filtrado laxo (PC → celular sí entra).

Traza de lo que pasaría (IPs de documentación):
```
Celular → STUN          CGNAT asigna 198.51.100.9:40001   (el PC recibe este candidato)
Celular → PC            CGNAT asigna 198.51.100.9:40517   (puerto nuevo, nadie lo conoce)

PC → 198.51.100.9:40001  el router de la casa abre su filtro SOLO para :40001
     y el paquete entra al celular por el mapeo de STUN (filtrado laxo)          ✓
Celular → PC desde :40517  el router no tiene filtro para :40517 → descarta     ✗
```
Media conexión: el PC llega al celular, pero el celular no llega al PC. Se registró igual en la
fase 2 (2026-11-01).

**Con UPnP** (router viejo) el agente le pedía al router "lo que llegue al puerto X, mándalo al
PC". Un puerto reenviado no filtra por origen, así que entraba el paquete desde `:40517` y el PC
respondía a esa dirección real. Por eso funcionaba sin pensar en nada de esto.

**Por qué no basta con reintentar:** el NAT se comporta igual en cada intento. Si no conecta en
unos segundos, no conecta nunca por ese camino.

## 3. ¿Todo P2P termina usando un relay?

No siempre: depende de cuántos peers hay.

- **BitTorrent** tiene cientos de peers y no necesita que todos se conecten con todos. Muchos tienen
  el puerto abierto (UPnP, reenvío manual o IP pública) y cualquiera les llega. Si dos peers detrás
  de NAT difícil no se conectan, cada uno baja de otros. La extensión BEP 55 (holepunch) usa a un
  tercer peer como presentador: funciona con NAT cono y falla con simétrico, y nadie lo nota.
- **Con exactamente 2 peers** (este proyecto), si esos dos no conectan no hay plan B. Por eso:

| Sistema | Directo | Respaldo |
|---|---|---|
| WebRTC (Meet, Discord) | ICE (RFC 8445) | TURN (RFC 8656) |
| Tailscale | punching agresivo + fuerza bruta de puertos | DERP (relay propio) |
| Skype antiguo | directo | otro usuario con buena red ("supernodo") |
| Consolas | directo | relay del fabricante |

Lo que se suele citar es que en WebRTC una minoría de las conexiones (del orden del 10–20 %) cae en
TURN. Es una cifra de referencia general, no una medición de este proyecto.

## 4. Caminos sin relay

### 4.1 Abrir un puerto en el router
- **UPnP IGD / NAT-PMP (RFC 6886) / PCP (RFC 6887):** el programa le pide el puerto al router.
  Es automático, pero depende de que el router lo tenga y lo traiga activado.
- **Reenvío manual:** es la misma regla, escrita a mano en la página del router
  (`rda-agent -port` / `RDA_PORT`). Hay que repetirla en cada router.

Las dos dependen del router. Son una buena optimización, pero no una solución general.

### 4.2 IPv6
Sin NAT, la dirección que el equipo ve en sí mismo es la que ve internet. Solo queda un firewall
con estado, que el punching abre sin adivinar nada:
```
t=0  PC  → [2001:db8:b::2]:51000  PING   (su firewall deja entrar respuestas de ese IP:puerto)
t=0  Cel → [2001:db8:a::1]:47900  PING   (el del celular hace lo mismo)
t≈RTT  los dos cruzan agujeros ya abiertos → PONG → conectado
```
Requiere IPv6 en **las dos** redes. El código ya lo soporta (candidato `ipv6`).

### 4.3 Fuerza bruta de puertos (paradoja del cumpleaños)

**La idea.** Si no se puede saber el puerto que el CGNAT asignará hacia el PC, se crean muchos y
se apuesta a que coincida alguno:
```
Celular (simétrico): abre N sockets; cada uno manda PING a PC_pub:47900
                     → el CGNAT crea N mapeos con puertos públicos p1…pN
PC (EIM, conserva puerto): manda M PINGs a Cel_pub en M puertos al azar q1…qM
                     → el router de la casa abre M filtros: (Cel_pub, qj)
Basta un pi = qj:  el PING del celular desde pi entra por el filtro qj → PONG → conectado.
```

**La cuenta.** Con R puertos posibles y los N puertos del celular al azar, cada disparo del PC
acierta con probabilidad N/R. Los M disparos fallan todos con probabilidad (1 − N/R)^M ≈ e^(−NM/R):

```
P(al menos una coincidencia) = 1 − e^(−N·M/R)
```

Con R ≈ 64 512 (puertos 1024–65535):

| N (sockets del celular) | M (disparos del PC) | P |
|---|---|---|
| 256 | 256 | ~64 % |
| 256 | 1024 | ~98 % |
| 256 | 2048 | ~99,97 % |

Para un objetivo p: M = −ln(1 − p) · R / N. Con N = 256 y p = 99 %, M ≈ 1160.
`core/natlab` (`HitChance`, `ProbesFor`) implementa estas fórmulas y tiene tests con estas cifras.

¿Por qué "cumpleaños"? Porque no se busca un puerto exacto sino **cualquier** coincidencia entre
dos conjuntos. Eso pasa mucho antes de lo que se intuye, como con 23 personas y dos cumpleaños
iguales.

**Costo por intento** (N = 256, M = 1024):

| Recurso | Costo |
|---|---|
| Paquetes | ~256 por ronda del celular + ~1024 del PC; unos miles en total |
| Bytes | ~100–200 KB (PING de ~40 B) |
| Tiempo | ~1–3 s |
| AWS | 0 (solo un par de mensajes de señalización más) |
| Batería | despreciable |

**Condiciones para que funcione:**
1. **Un lado tiene que ser EIM.** Si los dos son simétricos, hay que adivinar dos puertos a la vez
   y la probabilidad se desploma: se pasa directo al relay.
2. **La misma IP pública para todos los sockets.** Si el CGNAT reparte entre varias IPs
   ("pooling" no pareado), el otro lado no sabe a qué IP disparar.
3. **Que nadie lo corte:**
   - Las operadoras limitan cuántos puertos puede usar cada cliente (a veces 1–2 mil): abrir 256
     consume una buena parte por unos segundos.
   - Los M disparos crean M entradas en la tabla del router de la casa.
   - Algún firewall puede tomarlo como un escaneo de puertos.

**Cómo reparte puertos el NAT cambia todo:**

| Asignación | R efectivo | Estrategia |
|---|---|---|
| Al azar | ~64 512 | la tabla de arriba |
| Por bloque por cliente (p. ej. 2048) | el tamaño del bloque | se dispara solo dentro del bloque: M ≈ 37 para 99 % con N = 256 |
| Secuencial | unas decenas | predecir el próximo puerto y probar una ventana chica |
| Conserva el puerto | 1 | no hace falta adivinar |

Por eso, antes de implementarla, se mide (sección 6).

## 5. Relay: el único camino que no depende de nada

Los dos lados le hablan **hacia afuera** a un servidor con IP pública, y ningún NAT bloquea eso. El
relay solo reenvía bytes. Como SSH va cifrado de punta a punta, el relay no ve el contenido.

Dónde correrlo (AWS):

| Servicio | ¿Sirve? | Nota |
|---|---|---|
| Lambda | ✗ | no puede escuchar un socket UDP entrante |
| App Runner | ✗ | solo HTTP |
| Fargate | ✓ | IP pública propia en la tarea; **sin NLB** (~16 USD/mes fijos) |
| EC2 / Lightsail | ✓ | VM; hay que mantener el sistema operativo |

Toda IPv4 pública encendida cuesta ~3,65 USD/mes (EC2 o Fargate). Por eso se enciende **solo
cuando hace falta**: la Lambda hace `RunTask` y la tarea se apaga sola tras 10 min sin tráfico.
La contra es el arranque en frío, de 30–60 s. Los precios están por verificar antes de construir.

## 6. Medirlo: `rda natlab`

`core/natlab` usa solo servidores STUN públicos: no necesita el PC ni tocar el router.

- **A. Un socket → 5 destinos.** Dos puertos de Google en la misma IP (19302 y 3478), más
  Cloudflare, Twilio y Nextcloud. Distingue EIM / ADM / APDM.
- **B. 32 sockets nuevos → un destino.** Mide si la IP pública es siempre la misma, si conserva
  el puerto, y los saltos entre sockets (secuencial / bloque / al azar).
- **C. Conclusión:** el rango efectivo y cuántos disparos harían falta para 99 %.

Uso:
- **PC:** `./bin/rda natlab -label "wifi casa"`, o con `-anon` para publicar.
- **Celular:** Pruebas → **Experimento NAT** (con el wifi apagado para medir el 4G). Copiar y
  Compartir entregan la versión anónima (tus IPs como `PUB1`, `LOCAL`).

Cómo se clasifica:
- **Mapeo:** se compara el público entre pares de destinos (misma IP con otro puerto, y otra IP).
- **Asignación:**
  - **Conserva el puerto:** al menos 80 % de los sockets con público = local.
  - **Secuencial:** mediana de |salto| ≤ 16.
  - **Bloque:** el rango observado es menor que 1/4 del esperado al azar, que es
    R·(N−1)/(N+1). El bloque se estima como rango·(N+1)/(N−1), redondeado a potencia de 2.
  - **Al azar:** en cualquier otro caso.

**Límites del experimento:**
- No mide el **filtrado** (hace falta un servidor que responda desde otra IP, como CHANGE-REQUEST
  de RFC 5780).
- Tampoco mide cuánto dura un mapeo sin tráfico.
- 32 muestras alcanzan para distinguir patrones gruesos; con un bloque o un azar dudosos,
  conviene repetir.

## 7. La escalera que se adoptó

Directo (LAN, IPv6, UPnP, puerto fijo, STUN) → fuerza bruta (solo si tiene sentido) → relay.
Los tiempos y criterios de fracaso de cada escalón están en PLAN.md, fase 5.

## 8. Lección de método: medir antes de construir

La hipótesis "4G simétrico" salió de **una** falla (2026-11-01) con una explicación plausible, y
se repitió en el plan como si fuera un hecho. Ese mismo día había otra causa a la vista
(`sendto: operation not permitted`: Android cortando la red en segundo plano). El experimento
de 5 s la refutó antes de escribir el refactor multi-socket de la fuerza bruta, que no hacía falta.

Resultado medido del 4G (anónimo, ver experimentos.md):
```
A. un socket → 5 destinos:   siempre PUB1:35524               → mapeo EIM
B. 32 sockets → google:      puertos 35330–35784, al azar     → bloque de ~512 por cliente
```
Y en un intento real conectó directo sin UPnP. Los logs mostraron además que el **filtrado** del
4G es independiente del origen: el PING del PC entró antes de que el celular le escribiera.

Un CGNAT EIM con bloque por cliente es lo que recomienda RFC 6888 (EIM para que funcionen las
apps P2P; bloques para poder registrar quién usó qué puerto sin anotar cada conexión).

> **Corrección (2026-11-11, tarde).** "El 4G es EIM" vale para esa medición, no para la operadora.
> Horas después de conectar 3/3, el mismo 4G midió **simétrico**, con bloque de ~256, y no conectó
> en ningún orden. El celular de un colega con la misma operadora dio lo mismo. La fuerza bruta
> sí hace falta para estas redes. Detalle en experimentos.md, "El 4G se volvió simétrico".

## 9. El orden importa: NAT de Linux "envenenado" (2026-11-11)

Caso real: el Mac conectado a la **zona Wi-Fi del celular** (4G) no lograba entrar a pc-casa, pero
pc-casa sí entraba al Mac, por la misma red y en cualquier momento. `rda natlab` en el Mac decía
que todo estaba bien (EIM, bloque de ~512, siempre la misma IP). Las 3 rondas de prueba dieron el
mismo resultado: dirección A ✅ 3/3, dirección B ❌ 3/3.

**Causa.** Entre el Mac y la operadora hay un NAT más: el de Android, que es el de Linux
(`conntrack` + `MASQUERADE`). Ese NAT anota en su tabla **cualquier** paquete que le llega, también
los que nadie pidió:
```
1. pc-casa dispara primero → su PING llega al celular antes de que el Mac haya escrito.
2. conntrack anota la conversación  pc-casa:42197 ⇄ celular:P  (entrante, sin dueño).
3. El Mac dispara a pc-casa:42197. MASQUERADE querría usar celular:P, pero esa
   combinación ya está ocupada → elige OTRO puerto P'.
4. pc-casa ve llegar al Mac (si llega) desde P', al que nunca le escribió → su router lo bota.
   Los PING de pc-casa siguen yendo a P → el celular los bota.   ⇒ trabado para siempre.
```
Si el Mac escribe **primero**, conntrack anota la salida con P, y el PING de pc-casa entra por ahí.

**Prueba de la hipótesis** (`rda punch` manual, con `-port` para que la dirección de pc-casa fuera
predecible y así controlar el orden):

| Prueba | Dispara primero | Resultado |
|---|---|---|
| 1 | el Mac (pc-casa 4 s después) | ✅ conectado, RTT 34–61 ms |
| 2 | pc-casa (el Mac ~4 s después, durante 25 s) | ❌ no cruzó nada |

**Arreglo.** El agente (que responde la offer) espera 1 s antes de su primer PING, o hasta que
le llegue uno del cliente (`punch.Session.ConnectAfter`, `rendezvous.ListenerFirstShot`). El
cliente dispara apenas recibe la answer. Resultado: Mac → pc-casa ✅ 3/3, a cambio de ~1 s más.

**Y si el del NAT de Linux es el agente** (por ejemplo, el Mac en la zona Wi-Fi atendiendo a
pc-casa), el orden ideal es el inverso. Por eso el cliente **reintenta**: si en 5 s
(`FirstAttempt`) no conecta, abre sockets nuevos y manda otra offer con `"order":"listener-first"`.
Esta vez el agente dispara al tiro y el cliente espera 1 s (`DialerHold`). Los sockets tienen que
ser nuevos: la tabla del NAT envenenado ya tiene anotada la combinación vieja. Un agente antiguo
ignora el campo y repite el orden normal, así que no rompe nada.

**Lección:** que `natlab` diga "EIM" no basta. Mide el mapeo hacia servidores que *nosotros*
contactamos primero; no ve qué pasa cuando el otro lado escribe antes. El orden de los disparos
es parte del protocolo, no un detalle.

## Referencias

- RFC 4787 — NAT Behavioral Requirements for Unicast UDP (mapeo y filtrado)
- RFC 5389 — STUN · RFC 5780 — NAT Behavior Discovery Using STUN
- RFC 8445 — ICE · RFC 8656 — TURN
- RFC 6886 — NAT-PMP · RFC 6887 — PCP · RFC 6888 — Requisitos para CGN
- BEP 55 — Holepunch extension (BitTorrent)
- Tailscale, "How NAT traversal works" (blog técnico; fuerza bruta de puertos y DERP)
- netfilter/conntrack de Linux: cómo MASQUERADE elige el puerto de origen cuando la tupla está ocupada
