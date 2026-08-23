#!/usr/bin/env bash
# Prueba de carga multiusuario de OKF Converter, con perfil de tráfico real.
#
# Mide lo que el README afirma y nadie había medido: que el API solo publica
# en la cola y responde de inmediato, que el trabajo pendiente se acumula en
# RabbitMQ en vez de dentro de una petición HTTP, y que escalar workers
# absorbe una hora punta sin tocar el API.
#
# La carga NO es una ráfaga. Llega repartida a lo largo de una ventana de diez
# minutos siguiendo un perfil de horas punta: dos picos idénticos separados por
# un valle, con las llegadas sorteadas como un proceso de Poisson de tasa
# variable —así que caen a rachas, con huecos, como el tráfico de verdad, y no
# a intervalos de metrónomo—. Los workers se escalan en el valle entre los dos
# picos, que es lo que haría un operador que ve venir la segunda punta: el pico
# 1 lo aguanta la escala de partida y el pico 2 la escalada, y como los dos
# picos se generan idénticos, comparar lo que costó cada uno es justo.
#
#   bash carga.sh
#   VENTANA=120 bash carga.sh              # humo del propio guion, ~3 min
#   PICO=15 VENTANA=900 bash carga.sh      # más duro y más largo
#
# Necesita el stack levantado (make up) y estar en la raíz del repo.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
[ -f docker-compose.yml ] || { echo "No encuentro docker-compose.yml junto a scripts/."; exit 1; }
set -a; . ./.env; set +a

BASE="${BASE:-${FRONTEND_URL:-http://localhost:8080}}"
PROM="http://localhost:${PROMETHEUS_PORT}"
RMQ="http://localhost:${RABBITMQ_UI_PORT}"
AUTH="${RABBITMQ_USER}:${RABBITMQ_PASSWORD}"

# Duración de la ventana de tráfico, en segundos. Todo el perfil se define en
# fracciones de ella, así que acortarla comprime los picos en vez de recortarlos.
VENTANA="${VENTANA:-600}"
# Cuentas concurrentes. Las llegadas se reparten entre ellas por turnos.
USUARIOS="${USUARIOS:-8}"
# Tasa de llegada en la cresta de cada pico y en el valle, en documentos/s.
# PICO tiene que quedar por encima de la capacidad de conversión (réplicas x
# concurrencia / duración media) o no se formará cola que observar.
PICO="${PICO:-8}"
VALLE="${VALLE:-0.3}"
# Réplicas de worker a las que se escala en el valle intermedio.
ESCALA="${ESCALA:-6}"
# Segundos de drenaje que se conceden DESPUÉS de cerrar la ventana.
LIMITE="${LIMITE:-300}"
# Semilla del sorteo de llegadas. Fija por defecto: dos corridas seguidas
# reciben exactamente el mismo tráfico y se pueden comparar entre sí.
SEMILLA="${SEMILLA:-1}"

WORK="$(mktemp -d)"
STAMP="$(date +%s)"
PASS=0
FAIL=0
AVISOS=0
ESCALADO=0

green() { printf '\033[32m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }
azul()  { printf '\033[36m%s\033[0m\n' "$*"; }
step()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }

ok()    { PASS=$((PASS+1)); green "  ok    $*"; }
bad()   { FAIL=$((FAIL+1)); red   "  FALLA $*"; }
aviso() { AVISOS=$((AVISOS+1)); azul "  aviso $*"; }

# --------------------------------------------------------------------------
# Intérprete de Python. Se busca ejecutándolo, no con `command -v`: en Windows
# hay un alias de la Microsoft Store llamado python3 que existe en el PATH, se
# deja encontrar, y lo único que hace al ejecutarse es imprimir «Python was not
# found». Comprobar solo que el comando existe da un preflight en verde y luego
# falla cada llamada, que es lo peor de los dos mundos.
# --------------------------------------------------------------------------
PY=""
detectar_python() {
  local c salida
  for c in python3 python "py -3"; do
    salida=$($c -c 'print("ok")' 2>/dev/null)
    if [ "$salida" = ok ]; then PY="$c"; return 0; fi
  done
  return 1
}

# --------------------------------------------------------------------------
# Helpers de consulta. metric() y sql() son los mismos de tolerancia.sh:
# copiados en vez de extraídos a un lib.sh común porque son veinte líneas y
# cada guion se lee mejor entero.
# --------------------------------------------------------------------------

# metric <expr> -> valor escalar (0 si la serie no existe todavía)
metric() {
  curl -sS --get "$PROM/api/v1/query" --data-urlencode "query=$1" 2>/dev/null |
    $PY -c 'import json,sys
try:
    r = json.load(sys.stdin)["data"]["result"]
    print(float(r[0]["value"][1]) if r else 0.0)
except Exception:
    print(0.0)'
}

# metric_por <expr> -> "etiqueta valor" por serie, para las métricas con labels
metric_por() {
  curl -sS --get "$PROM/api/v1/query" --data-urlencode "query=$1" 2>/dev/null |
    $PY -c 'import json,sys
try:
    for s in json.load(sys.stdin)["data"]["result"]:
        m = s["metric"]
        print(list(m.values())[0] if m else "-", s["value"][1])
except Exception:
    pass'
}

sql() { docker compose exec -T db psql -qtAX -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "$1" 2>/dev/null; }

# cola_pendiente <nombre> -> mensajes pendientes en esa cola.
#
# Se lee de la API de gestión de RabbitMQ y no de Prometheus a propósito: la
# forma de la cola durante un pico es justo lo que se quiere ver, y Prometheus
# raspa cada 15 s. Tampoco por `docker compose exec rabbitmqctl`, que tarda casi
# un segundo por llamada y no se puede muestrear a este ritmo.
cola_pendiente() {
  curl -sS -u "$AUTH" "$RMQ/api/queues/%2F/$1" 2>/dev/null |
    grep -o '"messages":[0-9]*' | head -1 | cut -d: -f2
}

# --------------------------------------------------------------------------
# Limpieza: los subshells de fondo no se mueren solos si se interrumpe el
# guion, y una escala a medias dejaría el stack distinto de como se encontró.
# --------------------------------------------------------------------------
restaurar_escala() {
  [ "$ESCALADO" = 1 ] || return 0
  ESCALADO=0
  echo "  restaurando worker=$WORKER_REPLICAS..."
  docker compose up -d --no-deps --scale worker="$WORKER_REPLICAS" worker >/dev/null 2>&1
}

parar_fondo() {
  touch "$WORK/alto"
  local p
  for p in $(jobs -p 2>/dev/null); do kill "$p" 2>/dev/null; done
  wait 2>/dev/null
}

interrumpido() {
  echo
  red "  interrumpido"
  parar_fondo
  restaurar_escala
  echo "  artefactos en: $WORK"
  exit 130
}
trap interrumpido INT TERM

# --------------------------------------------------------------------------
step "0. Preflight"
# --------------------------------------------------------------------------
if ! detectar_python; then
  red "Hace falta Python 3 (lo usan también smoke.sh y tolerancia.sh)."
  red "Se probó 'python3', 'python' y 'py -3' y ninguno ejecuta."
  exit 1
fi
ok "intérprete de Python: $PY"

code=$(curl -sS -o /dev/null -w '%{http_code}' "$BASE/api/health" 2>/dev/null)
if [ "$code" != 200 ]; then
  red "El stack no está arriba. Corre 'make up' primero."
  exit 1
fi
ok "la API responde en $BASE"

REPLICAS_INICIO=$(docker compose ps worker -q 2>/dev/null | wc -l | tr -d ' ')
echo "  workers al arrancar: $REPLICAS_INICIO x $WORKER_CONCURRENCY concurrencia = $((REPLICAS_INICIO * WORKER_CONCURRENCY)) conversiones en paralelo"

# Los contadores de Prometheus son acumulativos y el stack puede venir de un
# `make smoke` previo, así que todo lo que se reporte al final es un delta
# contra esta línea base.
BASE_ENQ=$(metric 'sum(convert_jobs_enqueued_total)')
BASE_PROC=$(metric 'sum(convert_jobs_processed_total)')
BASE_SKIP=$(metric 'sum(convert_jobs_skipped_total)')
BASE_RETRY=$(metric 'sum(convert_jobs_retried_total)')
BASE_DEAD=$(metric 'sum(convert_jobs_dead_lettered_total)')
echo "  línea base: encolados=$BASE_ENQ procesados=$BASE_PROC descartes=$BASE_DEAD"

# --------------------------------------------------------------------------
step "1. Perfil de tráfico"
# --------------------------------------------------------------------------
# Se sortean las llegadas ANTES de empezar, no sobre la marcha: así el perfil es
# el mismo lo rápido o lento que vaya la máquina, y dos corridas con la misma
# semilla reciben tráfico idéntico y se pueden comparar.
$PY - "$WORK" "$VENTANA" "$PICO" "$VALLE" "$USUARIOS" "$SEMILLA" <<'PYEOF'
import math, os, random, sys

work, ventana, pico, valle, usuarios, semilla = sys.argv[1:7]
V = float(ventana); PICO = float(pico); VALLE = float(valle)
U = int(usuarios)
random.seed(int(semilla))

# Dos picos idénticos: uno a un cuarto de la ventana y otro al 70 %. Tienen que
# ser idénticos porque la comparación de escalado los enfrenta; el valle entre
# ambos es lo bastante ancho para escalar sin pillar ninguna cresta.
C1, C2, W = 0.25 * V, 0.70 * V, 0.06 * V


def forma(d):
    """Peso del pico a distancia d de su cresta: coseno alzado, 1 en el centro."""
    return 0.5 * (1 + math.cos(math.pi * d / W)) if d < W else 0.0


# Poisson de tasa variable por adelgazamiento: se sortean candidatos a la tasa
# maxima y se acepta cada uno con probabilidad forma(t). Es lo que da el aspecto
# disperso -rachas y huecos- en vez de un goteo regular.
#
# El pico se sortea UNA SOLA VEZ y se coloca igual en los dos centros. Que los
# dos picos sigan la misma distribucion no basta: con picos estrechos el azar
# manda, y una corrida puede darle al pico 2 el doble de carga que al pico 1 y
# apuntarselo al escalado. Copiando la misma realizacion, el exceso de los dos
# picos es identico byte a byte; lo unico que los separa es el ruido de valle
# que cae dentro de cada ventana, que son unas pocas llegadas sobre cientos.
pico_rel, t = [], -W
while True:
    t += random.expovariate(PICO - VALLE)
    if t >= W:
        break
    if random.random() < forma(abs(t)):
        pico_rel.append(t)

# El valle, en cambio, es ruido de fondo independiente en toda la ventana.
llegadas, t = [], 0.0
while True:
    t += random.expovariate(VALLE)
    if t >= V:
        break
    llegadas.append(t)

for c in (C1, C2):
    llegadas.extend(c + d for d in pico_rel if 0.0 <= c + d < V)
llegadas.sort()

# Reparto por turnos entre las cuentas: todas ven el pico a la vez, que es lo
# que se quiere. Es una hora punta, no una cuenta trabajando de más.
for i in range(1, U + 1):
    with open(os.path.join(work, "plan-u%d" % i), "w", newline=chr(10)) as fh:
        for t in llegadas[i - 1::U]:
            fh.write("%d\n" % int(t * 1000))

with open(os.path.join(work, "perfil.env"), "w", newline=chr(10)) as fh:
    fh.write("C1=%d\nC2=%d\nW=%d\nT_ESCALA=%d\nPLANEADOS=%d\n"
             % (C1, C2, W, (C1 + C2) / 2, len(llegadas)))

# Vista previa del perfil, para ver lo que se va a lanzar antes de lanzarlo.
cols, filas = 58, 7
cubos = [0.0] * cols
for t in llegadas:
    cubos[min(cols - 1, int(t / V * cols))] += 1
ancho = V / cols
cubos = [c / ancho for c in cubos]
tope = max(max(cubos), PICO)
print("  llegadas planeadas: %d documentos en %d s (media %.2f doc/s)"
      % (len(llegadas), V, len(llegadas) / V))
en_pico = lambda c: sum(1 for t in llegadas if c - W <= t <= c + W)
print("  picos a los %d s y %d s, anchos %d s; escalado a los %d s"
      % (C1, C2, 2 * W, (C1 + C2) / 2))
print("  llegadas dentro de cada pico: %d y %d (mismo exceso sorteado;"
      " la diferencia es ruido de valle)" % (en_pico(C1), en_pico(C2)))
print()
for f in range(filas, 0, -1):
    linea = "".join("#" if c / tope * filas >= f - 0.5 else " " for c in cubos)
    print("  %5.1f | %s" % (tope * f / filas, linea))
print("  %5s +%s" % ("0.0", "-" * cols))
marca = [" "] * cols
for c, etq in ((C1, "pico 1"), (C2, "pico 2")):
    p = int(c / V * cols) - len(etq) // 2
    for k, ch in enumerate(etq):
        if 0 <= p + k < cols:
            marca[p + k] = ch
e = int(((C1 + C2) / 2) / V * cols)
if 0 <= e < cols and marca[e] == " ":
    marca[e] = "^"
print("  %5s  %s" % ("", "".join(marca)))
print("  %5s  doc/s en cubos de %.0f s   (^ = momento del escalado)" % ("", ancho))
PYEOF
[ -f "$WORK/perfil.env" ] || { red "No se pudo generar el perfil de tráfico."; exit 1; }
. "$WORK/perfil.env"

# --------------------------------------------------------------------------
step "2. Preparación"
# --------------------------------------------------------------------------
mkdir -p "$WORK/docs"

# Documentos sintéticos, tres perfiles que se van ciclando. Los de ejemplos/
# pesan menos de 3 KB: sirven para demostrar el pipeline, no para estresarlo.
# Los largos son los que hacen que un pico cueste trabajo de verdad y los que
# ejercitan la segmentación por encabezados.
$PY - "$WORK/docs" <<'PYEOF'
import os, sys

destino = sys.argv[1]
parrafo = (
    "La conversion recorre el documento buscando la estructura que ya trae, "
    "sin inventar divisiones donde el autor no las puso. Cada unidad logica "
    "acaba siendo un concepto del bundle, con su frontmatter y sus enlaces "
    "al indice y a sus vecinos. "
)

perfiles = [("corto", 4, 900), ("medio", 20, 2200), ("largo", 60, 3200)]

for idx, (nombre, secciones, tam) in enumerate(perfiles):
    partes = ["# Documento de carga (%s)\n\n" % nombre,
              "Generado por scripts/carga.sh para la prueba de carga.\n\n"]
    for s in range(1, secciones + 1):
        partes.append("## Seccion %d\n\n" % s)
        cuerpo = (parrafo * (tam // len(parrafo) + 1))[:tam]
        partes.append(cuerpo + "\n\n")
    ruta = os.path.join(destino, "%d.md" % idx)
    with open(ruta, "w", encoding="utf-8", newline=chr(10)) as fh:
        fh.write("".join(partes))
    print("  %-6s %7d bytes  %2d secciones" % (nombre, os.path.getsize(ruta), secciones))
PYEOF

# Cada usuario se registra una vez y se le guarda el JWT suelto. El resto del
# test manda la cookie a mano con -H y NUNCA vuelve a escribir una jarra:
# subidor y sondeador corren a la vez para el mismo usuario, y dos curl
# escribiendo el mismo archivo de cookies con -c se pisan a media escritura. La
# jarra queda truncada y las peticiones siguientes salen con 401. Costó un rato
# entender que el 401 lo producía el propio test y no el API.
for i in $(seq 1 "$USUARIOS"); do
  correo="carga-$STAMP-$i@example.com"
  code=$(curl -sS -c "$WORK/u$i.jar" -X POST "$BASE/api/auth/register" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"Carga $i\",\"email\":\"$correo\",\"password\":\"contrasena-larga\"}" \
    -o /dev/null -w '%{http_code}')
  [ "$code" = 201 ] || bad "no se pudo registrar $correo (HTTP $code)"
  # Formato Netscape: dominio, flag, ruta, secure, expiración, nombre, valor.
  awk '$6=="auth_token"{print $7}' "$WORK/u$i.jar" > "$WORK/tok-u$i"
  [ -s "$WORK/tok-u$i" ] || bad "no se obtuvo el JWT de $correo"
  : > "$WORK/prog-u$i"
  echo 0 > "$WORK/conf-u$i"
done
[ "$FAIL" = 0 ] && ok "$USUARIOS cuentas registradas, JWT en mano"

# --------------------------------------------------------------------------
# Generadores de carga
# --------------------------------------------------------------------------

# subidor <n usuario> — sigue el calendario de su cuenta: espera hasta cada
# instante programado y entonces sube un documento (upload-url, PUT a MinIO,
# confirm). Si el sistema se atasca, el subidor llega tarde a su propia cita en
# vez de saltársela, y ese retraso se mide: es la señal de que el cliente ya no
# consigue imponer el perfil que quería.
subidor() {
  local i="$1"
  local galleta="Cookie: auth_token=$(cat "$WORK/tok-u$1")"
  local n=0 off espera seg mil
  local doc nombre tam r code t fid url confirmados=0

  while read -r off; do
    off=${off%$'\r'}
    n=$((n + 1))
    espera=$(( T0_MS + off - $(date +%s%3N) ))
    if [ "$espera" -gt 0 ]; then
      printf -v seg '%d' $((espera / 1000))
      printf -v mil '%03d' $((espera % 1000))
      sleep "$seg.$mil"
    fi
    echo "$(( $(date +%s%3N) - T0_MS - off ))" >> "$WORK/desfase-u$i.txt"

    doc="$WORK/docs/$(( (n - 1) % 3 )).md"
    nombre="carga-$STAMP-u$i-$n.md"
    tam=$(wc -c < "$doc" | tr -d ' ')

    r=$(curl -sS -H "$galleta" -X POST "$BASE/api/files/upload-url" \
      -H 'Content-Type: application/json' \
      -d "{\"filename\":\"$nombre\",\"contentType\":\"text/markdown\",\"size\":$tam}" \
      -o "$WORK/uu-u$i.json" -w '%{http_code} %{time_total}')
    code=${r%% *}; t=${r##* }
    echo "$code $t" >> "$WORK/lat-uploadurl-u$i.txt"
    if [ "$code" != 200 ]; then
      echo "upload-url $nombre HTTP $code" >> "$WORK/err-u$i.txt"
      continue
    fi

    # El cuerpo se parsea con Python y no con sed porque Go escapa los '&' de la
    # URL prefirmada como &: una extracción a mano firmaría mal.
    read -r fid url <<<"$($PY -c 'import json,sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
print(d["file"]["id"], d["uploadUrl"])' "$WORK/uu-u$i.json" 2>/dev/null)"
    if [ -z "${url:-}" ]; then
      echo "upload-url $nombre respuesta ilegible" >> "$WORK/err-u$i.txt"
      continue
    fi

    # Los bytes van directos a MinIO (localhost:9000), no por el proxy de nginx,
    # y con el mismo Content-Type con el que se firmó la URL.
    r=$(curl -sS -X PUT "$url" -H 'Content-Type: text/markdown' \
      --data-binary @"$doc" -o /dev/null -w '%{http_code} %{time_total}')
    code=${r%% *}; t=${r##* }
    echo "$code $t" >> "$WORK/lat-put-u$i.txt"
    if [ "$code" != 200 ]; then
      echo "put $nombre HTTP $code" >> "$WORK/err-u$i.txt"
      continue
    fi

    r=$(curl -sS -H "$galleta" -X POST "$BASE/api/files/$fid/confirm" \
      -o /dev/null -w '%{http_code} %{time_total}')
    code=${r%% *}; t=${r##* }
    echo "$code $t" >> "$WORK/lat-confirm-u$i.txt"
    # Un 503 aquí es el caso que interesa cazar: el documento quedó guardado
    # pero no se pudo encolar, así que ningún worker lo va a recoger.
    if [ "$code" = 200 ]; then
      # Solo un confirm con 200 puso trabajo en la cola. El drenaje espera esta
      # cuenta y no la planeada: si una subida falló, esperar el número teórico
      # dejaría el bucle colgado hasta LIMITE aguardando un trabajo que nunca se
      # encoló, y el reporte culparía al drenaje de un fallo del cliente.
      confirmados=$((confirmados + 1))
      echo "$confirmados" > "$WORK/conf-u$i"
    else
      echo "confirm $nombre HTTP $code" >> "$WORK/err-u$i.txt"
    fi
  done < "$WORK/plan-u$i"
}

# sondeador <n usuario> — sondea GET /api/files cada 2,5 s, exactamente como el
# dashboard del navegador. Es a la vez la carga de lectura realista (N usuarios
# conectados son un caudal constante que corre encima de las subidas) y el
# contador de progreso del test, sin pedirle nada extra al API.
sondeador() {
  local i="$1" r
  local galleta="Cookie: auth_token=$(cat "$WORK/tok-u$1")"
  while [ ! -f "$WORK/alto" ]; do
    r=$(curl -sS -H "$galleta" "$BASE/api/files" \
      -o "$WORK/ls-u$i.json" -w '%{http_code} %{time_total}')
    echo "$r" >> "$WORK/lat-list-u$i.txt"
    if [ "${r%% *}" = 200 ]; then
      # Se escribe aparte y se mueve encima para que el bucle principal nunca
      # lea un archivo a medio escribir. El temporal NO puede llamarse
      # prog-u$i.tmp: el glob prog-u* lo recogería y cada usuario contaría dos
      # veces.
      $PY -c 'import json,sys
sys.stdout.reconfigure(newline=chr(10))
d = json.load(open(sys.argv[1], encoding="utf-8"))
f = d.get("files", [])
print(sum(1 for x in f if x["status"] in ("converted", "failed")),
      sum(1 for x in f if x["status"] == "failed"),
      len(f))' "$WORK/ls-u$i.json" > "$WORK/tmp-prog-u$i" 2>/dev/null &&
        mv "$WORK/tmp-prog-u$i" "$WORK/prog-u$i"
    fi
    sleep 2.5
  done
}

# progreso -> "terminados fallados totales" sumando lo que ven los sondeadores
progreso() {
  cat "$WORK"/prog-u* 2>/dev/null |
    awk '{a+=$1; b+=$2; c+=$3} END {print a+0, b+0, c+0}'
}

# --------------------------------------------------------------------------
step "3. Ventana de tráfico — ${VENTANA}s, $USUARIOS usuarios, escalado a los ${T_ESCALA}s"
# --------------------------------------------------------------------------
rm -f "$WORK/alto" "$WORK"/tmp-prog-u*
: > "$WORK/serie.txt"

read -r TERM_INICIO _ _ <<<"$(progreso)"
PICO_COLA=0
DRENADA=0
ESCALADO_EN=""

T0=$SECONDS
T0_MS=$(date +%s%3N)
for i in $(seq 1 "$USUARIOS"); do sondeador "$i" & done
pids_sub=()
for i in $(seq 1 "$USUARIOS"); do subidor "$i" & pids_sub+=("$!"); done

ultima=""
while :; do
  el=$((SECONDS - T0))
  pend=$(cola_pendiente file_conversion)
  pend=${pend:-0}
  read -r term fall tot <<<"$(progreso)"
  confirmados=$(cat "$WORK"/conf-u* 2>/dev/null | awk '{s+=$1} END {print s+0}')
  echo "$el $pend $((term - TERM_INICIO)) $confirmados" >> "$WORK/serie.txt"
  [ "$pend" -gt "$PICO_COLA" ] 2>/dev/null && PICO_COLA=$pend

  # Escalado en el valle entre los dos picos. Se lanza en segundo plano porque
  # `docker compose up --scale` tarda varios segundos y bloquearlo aquí abriría
  # un hueco en el muestreo justo en mitad de la ventana.
  if [ -z "$ESCALADO_EN" ] && [ "$el" -ge "$T_ESCALA" ]; then
    ( docker compose up -d --no-deps --scale worker="$ESCALA" worker >/dev/null 2>&1 ) &
    ESCALADO=1
    ESCALADO_EN=$el
  fi

  subiendo=0
  for p in "${pids_sub[@]}"; do kill -0 "$p" 2>/dev/null && subiendo=1; done

  linea=$(printf '  %-8s t=%4ss  subiendo %s  encolados %-5s convertidos %-5s cola %-5s' \
    "$([ "$el" -lt "$VENTANA" ] && echo ventana || echo drenaje)" \
    "$el" "$([ "$subiendo" = 1 ] && echo si || echo no)" \
    "$confirmados" "$((term - TERM_INICIO))" "$pend")
  # En una terminal se reescribe la misma línea; redirigido a un archivo, \r no
  # borra nada y saldrían cientos de líneas casi idénticas.
  if [ -t 1 ]; then
    printf '\r%s   ' "$linea"
  elif [ "$linea" != "$ultima" ]; then
    printf '%s\n' "$linea"
    ultima="$linea"
  fi

  if [ "$el" -ge "$VENTANA" ] && [ "$subiendo" = 0 ] &&
     [ "$((term - TERM_INICIO))" -ge "$confirmados" ] 2>/dev/null; then
    DRENADA=1
    break
  fi
  [ "$el" -ge "$((VENTANA + LIMITE))" ] && break
  sleep 2
done
DURACION=$((SECONDS - T0))
CONFIRMADOS=$confirmados
[ -t 1 ] && echo

parar_fondo

if [ "$DRENADA" = 1 ]; then
  ok "los $CONFIRMADOS documentos encolados se convirtieron; todo drenado a los ${DURACION}s"
else
  bad "quedó trabajo sin drenar ${LIMITE}s después de cerrar la ventana"
fi
echo "  cola máxima durante la corrida: $PICO_COLA mensajes"
[ -n "$ESCALADO_EN" ] && echo "  escalado a $ESCALA workers a los ${ESCALADO_EN}s"

# --------------------------------------------------------------------------
step "4. Cómo respondió el sistema"
# --------------------------------------------------------------------------
$PY - "$WORK/serie.txt" "$VENTANA" "$C1" "$C2" "$W" "${ESCALADO_EN:--1}" <<'PYEOF'
import sys

ruta, ventana, c1, c2, w, esc = sys.argv[1:7]
V = float(ventana); C1 = float(c1); C2 = float(c2); W = float(w); ESC = float(esc)

filas = []
for linea in open(ruta):
    p = linea.split()
    if len(p) == 4:
        filas.append(tuple(int(x) for x in p))
if not filas:
    print("  sin muestras")
    raise SystemExit

fin = max(filas[-1][0], int(V))
cols = 58
ancho = fin / float(cols)


def grafico(titulo, valores, altura=7):
    tope = max([v for _, v in valores]) or 1
    print("  %s" % titulo)
    for f in range(altura, 0, -1):
        fila = "".join("#" if v / tope * altura >= f - 0.5 else " " for _, v in valores)
        print("  %6.1f | %s" % (tope * f / altura, fila))
    print("  %6s +%s" % ("0", "-" * cols))
    marca = [" "] * cols
    for c, etq in ((C1, "pico 1"), (C2, "pico 2")):
        p = int(c / ancho) - len(etq) // 2
        for k, ch in enumerate(etq):
            if 0 <= p + k < cols:
                marca[p + k] = ch
    if ESC >= 0:
        e = int(ESC / ancho)
        if 0 <= e < cols and marca[e] == " ":
            marca[e] = "^"
    v = int(V / ancho)
    if 0 <= v < cols and marca[v] == " ":
        marca[v] = "|"
    print("  %6s  %s" % ("", "".join(marca)))


# Llegadas: derivada de los confirmados acumulados, en documentos por segundo.
cubos = [[0.0, 0.0] for _ in range(cols)]
prev_t, prev_c = 0, 0
for t, pend, conv, conf in filas:
    k = min(cols - 1, int(t / ancho))
    cubos[k][0] += conf - prev_c
    cubos[k][1] += t - prev_t
    prev_t, prev_c = t, conf
grafico("Llegadas a la cola (doc/s)",
        [(i, (n / d if d else 0.0)) for i, (n, d) in enumerate(cubos)])
print()

# Cola: valor instantáneo, se toma el máximo de cada columna.
cola = [0] * cols
for t, pend, conv, conf in filas:
    k = min(cols - 1, int(t / ancho))
    cola[k] = max(cola[k], pend)
grafico("Trabajo pendiente en file_conversion (mensajes)", list(enumerate(cola)))
print("  %6s  ^ escalado    | cierre de la ventana    cubos de %.0f s" % ("", ancho))
print()


# Comparación de los dos picos: cuánto se acumuló y cuánto costó recuperarse.
def analiza(centro):
    ini, fin_p = centro - W, centro + W
    tope = max([p for t, p, _, _ in filas if ini <= t <= fin_p] or [0])
    vuelta = None
    for t, p, _, _ in filas:
        if t > fin_p and p == 0:
            vuelta = int(t - fin_p)
            break
    return tope, vuelta


t1, r1 = analiza(C1)
t2, r2 = analiza(C2)
print("  %-30s %12s %12s" % ("", "pico 1", "pico 2"))
print("  %-30s %12s %12s" % ("cola máxima (mensajes)", t1, t2))
print("  %-30s %12s %12s" % ("en vaciarse tras el pico",
                             "%ss" % r1 if r1 is not None else "no llegó a 0",
                             "%ss" % r2 if r2 is not None else "no llegó a 0"))
with open(ruta + ".picos", "w", newline=chr(10)) as fh:
    fh.write("PICO1_COLA=%d\nPICO2_COLA=%d\nPICO1_REC=%d\nPICO2_REC=%d\n"
             % (t1, t2, r1 if r1 is not None else -1, r2 if r2 is not None else -1))
PYEOF
[ -f "$WORK/serie.txt.picos" ] && . "$WORK/serie.txt.picos"

# --------------------------------------------------------------------------
step "5. Efecto del escalado"
# --------------------------------------------------------------------------
echo "  el pico 1 lo absorbieron $REPLICAS_INICIO workers ($((REPLICAS_INICIO * WORKER_CONCURRENCY)) en paralelo)"
echo "  el pico 2, $ESCALA workers ($((ESCALA * WORKER_CONCURRENCY)) en paralelo), con el mismo tráfico"

if [ "${PICO1_COLA:-0}" -le 2 ] && [ "${PICO2_COLA:-0}" -le 2 ]; then
  # Sin cola no hay nada que escalar: los workers convirtieron cada documento
  # según llegaba, así que sobraba capacidad ya en el pico 1 y añadir réplicas
  # no podía cambiar nada. Comparar aquí mediría ruido y lo llamaría escalado.
  aviso "la cola no llegó a formarse en ninguno de los dos picos: sube PICO por encima de la capacidad de conversión"
elif [ "${PICO2_COLA:-0}" -lt "${PICO1_COLA:-0}" ]; then
  ok "$(awk -v a="${PICO1_COLA:-0}" -v b="${PICO2_COLA:-0}" \
    'BEGIN{printf "con más workers el mismo pico acumuló %.0f%% menos cola (%d -> %d mensajes)", (1-b/a)*100, a, b}')"
else
  # No se falla por esto: en una máquina con pocos núcleos las réplicas compiten
  # por la misma CPU, y un test que falla por el hardware del que lo corre no
  # sirve para nada.
  aviso "escalar no redujo la cola del pico (${PICO1_COLA:-0} -> ${PICO2_COLA:-0} mensajes). Suele ser CPU del host, no la arquitectura"
fi

# --------------------------------------------------------------------------
step "6. Latencia del API vista por el cliente"
# --------------------------------------------------------------------------
# El API nunca espera a la conversión: si esto es cierto, los percentiles se
# mantienen bajos aunque la cola esté llena de trabajo pendiente.
percentiles() {
  local etiqueta="$1" prefijo="$2"
  cat "$WORK"/lat-"$prefijo"-u*.txt 2>/dev/null |
    $PY -c 'import sys
tiempos, codigos = [], {}
for linea in sys.stdin:
    p = linea.split()
    if len(p) != 2:
        continue
    codigos[p[0]] = codigos.get(p[0], 0) + 1
    try:
        tiempos.append(float(p[1]) * 1000)
    except ValueError:
        pass
if not tiempos:
    print("  %-14s sin datos" % sys.argv[1])
    raise SystemExit
tiempos.sort()
def pct(q):
    return tiempos[min(len(tiempos) - 1, int(q * len(tiempos)))]
resumen = " ".join("%s:%d" % kv for kv in sorted(codigos.items()))
print("  %-14s n=%-5d p50 %7.1f ms   p95 %7.1f ms   p99 %7.1f ms   max %7.1f ms   [%s]"
      % (sys.argv[1], len(tiempos), pct(.50), pct(.95), pct(.99), tiempos[-1], resumen))' "$etiqueta"
}

percentiles "upload-url" uploadurl
percentiles "PUT MinIO"  put
percentiles "confirm"    confirm
percentiles "GET /files" list
echo "  (PUT MinIO va directo a localhost:$MINIO_PUBLIC_PORT: esos bytes nunca pasan por el API)"

# El desfase entre la hora a la que tocaba subir y la hora a la que se subió. Si
# crece, el cuello de botella está en el generador y no en el sistema: el perfil
# que se está midiendo ya no es el que se planeó.
cat "$WORK"/desfase-u*.txt 2>/dev/null |
  $PY -c 'import sys
v = sorted(int(x) for x in sys.stdin if x.strip().lstrip("-").isdigit())
if v:
    print("  %-14s n=%-5d p50 %7d ms   p95 %7d ms   max %7d ms"
          % ("desfase", len(v), v[len(v)//2], v[min(len(v)-1, int(.95*len(v)))], v[-1]))'
echo "  (desfase = retraso del generador sobre su propio calendario; si es grande,"
echo "   el perfil que se midió fue más plano que el que se planeó)"

# --------------------------------------------------------------------------
step "7. Lo que dice el servidor"
# --------------------------------------------------------------------------
# Prometheus raspa cada 15 s, así que se le da tiempo a alcanzar al cliente
# antes de comparar. Sin esto la comparación falla sin que falle nada.
echo -n "  esperando a que Prometheus alcance al cliente"
for _ in $(seq 1 20); do
  proc=$(metric 'sum(convert_jobs_processed_total)')
  enq=$(metric 'sum(convert_jobs_enqueued_total)')
  d=$(awk -v a="$proc" -v b="$BASE_PROC" 'BEGIN{printf "%d", a-b}')
  e=$(awk -v a="$enq" -v b="$BASE_ENQ" 'BEGIN{printf "%d", a-b}')
  # Se espera a los DOS contadores: el del API y el de los workers viven
  # en procesos distintos y cada uno se raspa por su cuenta.
  [ "$d" -ge "$CONFIRMADOS" ] && [ "$e" -ge "$CONFIRMADOS" ] 2>/dev/null && break
  printf '.'
  sleep 3
done
echo

delta() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%d", a-b}'; }

ENQ=$(delta "$(metric 'sum(convert_jobs_enqueued_total)')" "$BASE_ENQ")
PROC=$(delta "$(metric 'sum(convert_jobs_processed_total)')" "$BASE_PROC")
SKIP=$(delta "$(metric 'sum(convert_jobs_skipped_total)')" "$BASE_SKIP")
RETRY=$(delta "$(metric 'sum(convert_jobs_retried_total)')" "$BASE_RETRY")
DEAD=$(delta "$(metric 'sum(convert_jobs_dead_lettered_total)')" "$BASE_DEAD")

printf '  %-34s %s\n' "encolados por el API"         "$ENQ"
printf '  %-34s %s\n' "procesados por los workers"   "$PROC"
printf '  %-34s %s\n' "entregas duplicadas saltadas" "$SKIP"
printf '  %-34s %s\n' "reintentos"                   "$RETRY"
printf '  %-34s %s\n' "descartados a .dead"          "$DEAD"

echo "  procesados por resultado (acumulado):"
metric_por 'sum by (status) (convert_jobs_processed_total)' | sed 's/^/    /'
echo "  bundles por veredicto (acumulado):"
metric_por 'sum by (verdict) (convert_bundles_validated_total)' | sed 's/^/    /'

p95=$(metric 'histogram_quantile(0.95, sum(rate(convert_job_duration_seconds_bucket[5m])) by (le))')
p50=$(metric 'histogram_quantile(0.50, sum(rate(convert_job_duration_seconds_bucket[5m])) by (le))')
awk -v a="$p50" -v b="$p95" \
  'BEGIN{printf "  %-34s p50 %.3f s   p95 %.3f s\n", "duración de la conversión", a+0, b+0}'
# convert_job_duration_seconds usa los DefBuckets de Prometheus, cuyo último
# bucket finito es 10 s. Un p95 pegado a 10 no es el valor real: es el techo.
awk -v v="$p95" 'BEGIN{exit !(v+0>=9.9)}' &&
  aviso "el p95 está en el techo de los buckets (10 s): la duración real es mayor. Ver convert_job_duration_seconds en metrics.go"

echo "  pendiente ahora mismo en las colas:"
for q in file_conversion file_conversion.retry file_conversion.dead; do
  printf '    %-24s %s\n' "$q" "$(cola_pendiente "$q")"
done

# Los workers vuelven a su escala AQUÍ y no al cerrar la ventana, que es donde
# parecía que tocaba: los contadores de Prometheus viven dentro de cada réplica,
# así que destruir las réplicas de más se lleva por delante todo lo que esas
# réplicas procesaron. El sum() se encoge y la cuadratura de «encolados =
# procesados + saltados» sale a deber por trabajos que sí se convirtieron. Es el
# mismo motivo por el que el dashboard lee el trabajo pendiente de RabbitMQ en
# vez de restar contadores (README, Observabilidad).
restaurar_escala

# --------------------------------------------------------------------------
step "8. Comprobaciones"
# --------------------------------------------------------------------------
peticiones=$(cat "$WORK"/lat-*.txt 2>/dev/null | wc -l | tr -d ' ')
errores=$(cat "$WORK"/err-u*.txt 2>/dev/null | wc -l | tr -d ' ')
if [ "$errores" = 0 ]; then
  ok "las $peticiones peticiones del test salieron sin errores"
else
  bad "$errores peticiones fallaron:"
  sort "$WORK"/err-u*.txt 2>/dev/null | uniq -c | sort -rn | head -10 | sed 's/^/        /'
fi

read -r TERM_FIN FALL_FIN TOT_FIN <<<"$(progreso)"
if [ "$FALL_FIN" = 0 ]; then
  ok "ningún archivo terminó en 'failed'"
else
  bad "$FALL_FIN archivos terminaron en 'failed'"
fi

if [ "$DEAD" -le 0 ] 2>/dev/null; then
  ok "nada acabó en la cola de descartes"
else
  bad "$DEAD mensajes acabaron en file_conversion.dead"
fi

# ¿Se perdió algún trabajo? La pregunta se le hace a Postgres y no a Prometheus.
# Los contadores de conversión viven dentro de cada réplica de worker: al bajar
# la escala desaparecen con ellas, y hasta sin tocar la escala van hasta 15 s por
# detrás del cliente. Sirven para mirar tendencias, no para cuadrar una cuenta
# exacta. La base de datos no tiene ninguno de los dos problemas.
convertidos_bd=$(sql "SELECT count(*) FROM files WHERE original_name LIKE 'carga-$STAMP-%' AND status = 'converted'" | tr -d ' \r')
if [ "${convertidos_bd:-0}" = "$CONFIRMADOS" ]; then
  ok "los $CONFIRMADOS documentos encolados figuran convertidos en Postgres: no se perdió ninguno"
else
  bad "solo $convertidos_bd de $CONFIRMADOS documentos llegaron a 'converted'"
fi

# La misma cuenta según Prometheus, ya solo como observación. Se suman los
# saltados aparte porque una entrega duplicada descartada es la idempotencia
# funcionando, no una pérdida.
if [ "$ENQ" -eq "$((PROC + SKIP))" ] 2>/dev/null; then
  ok "y las métricas cuadran: encolados = procesados + saltados ($ENQ = $PROC + $SKIP)"
else
  aviso "las métricas no cuadran (encolados=$ENQ, procesados=$PROC, saltados=$SKIP): retraso de raspado o contadores de réplicas ya destruidas, no trabajo perdido"
fi

# Aislamiento bajo carga: con USUARIOS cuentas subiendo a la vez, es la prueba
# de que el user_id en la consulta SQL aguanta la concurrencia.
#
# Se comprueba el techo, no la igualdad: ver MÁS de sus propios documentos es
# una fuga y no puede ser otra cosa, mientras que ver menos solo significa que
# alguna subida de ese usuario falló —algo que ya reporta el chequeo de
# errores—. Atar el aislamiento al número exacto haría que un fallo de red del
# cliente se reportara como una fuga de datos, la peor confusión posible aquí.
fugas=0
faltantes=0
for i in $(seq 1 "$USUARIOS"); do
  planeados=$(wc -l < "$WORK/plan-u$i" | tr -d ' ')
  n=$(awk '{print $3}' "$WORK/prog-u$i" 2>/dev/null)
  n=${n:-0}
  if [ "$n" -gt "$planeados" ]; then
    fugas=$((fugas + 1))
    echo "        usuario $i ve $n archivos y solo tenía $planeados en su calendario"
  elif [ "$n" -lt "$planeados" ]; then
    faltantes=$((faltantes + 1))
  fi
done
if [ "$fugas" = 0 ]; then
  ok "ningún usuario ve archivos que no sean suyos"
else
  bad "$fugas usuarios ven más archivos de los que subieron"
fi
[ "$faltantes" = 0 ] || aviso "$faltantes usuarios ven menos de lo planeado: subidas que fallaron, ver arriba"

# Sonda directa de aislamiento, como el paso 6 de smoke.sh: el primer usuario
# pide un archivo del segundo por id. La lista puede coincidir por casualidad;
# esto no.
ajeno=$($PY -c 'import json,sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
f = d.get("files", [])
print(f[0]["id"] if f else "")' "$WORK/ls-u2.json" 2>/dev/null)
if [ -n "$ajeno" ]; then
  code=$(curl -sS -H "Cookie: auth_token=$(cat "$WORK/tok-u1")" \
    -o /dev/null -w '%{http_code}' "$BASE/api/files/$ajeno")
  [ "$code" = 404 ] && ok "pedir el archivo de otro usuario por id responde 404" \
                    || bad "pedir un archivo ajeno respondió $code, esperaba 404"
fi

echo "  estado en Postgres:"
sql "SELECT status, count(*) FROM files WHERE original_name LIKE 'carga-$STAMP-%' GROUP BY status ORDER BY 1" |
  tr '|' ' ' | awk '{printf "    %-12s %s\n", $1, $2}'

# --------------------------------------------------------------------------
printf '\n\033[1m== Resumen\033[0m\n'
green "  $PASS comprobaciones ok"
[ "$AVISOS" -gt 0 ] && azul "  $AVISOS avisos"
[ "$FAIL" -gt 0 ] && red "  $FAIL fallas"
echo
echo "  El test deja $CONFIRMADOS archivos con sus bundles en MinIO y Postgres."
echo "  No se borran a propósito: el dashboard «Conversión de documentos» de"
echo "  Grafana (http://localhost:$GRAFANA_PORT) es medio motivo de correr esto."
echo "  Para dejarlo limpio: make reset"
echo "  artefactos en: $WORK"
exit $(( FAIL > 0 ))
