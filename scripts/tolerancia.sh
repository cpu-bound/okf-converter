#!/usr/bin/env bash
# Tolerancia a fallos e idempotencia — la parte del §6 que no se ve desde la
# interfaz. Ejecutar DESPUÉS de smoke.sh, con el stack arriba.
#
#   bash tolerancia.sh
#
# Necesita estar en la raíz del repo (usa docker compose y .env).
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
[ -f docker-compose.yml ] || { echo "No encuentro docker-compose.yml junto a scripts/."; exit 1; }
set -a; . ./.env; set +a

PROM="http://localhost:${PROMETHEUS_PORT}"
RMQ="http://localhost:${RABBITMQ_UI_PORT}"
AUTH="${RABBITMQ_USER}:${RABBITMQ_PASSWORD}"

green() { printf '\033[32m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }
step()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }

# metric <expr> -> valor escalar (0 si la serie no existe todavía)
metric() {
  curl -sS --get "$PROM/api/v1/query" --data-urlencode "query=$1" |
    python3 -c 'import json,sys
r=json.load(sys.stdin)["data"]["result"]
print(float(r[0]["value"][1]) if r else 0.0)'
}

# wait_metric <expr> <base> [limite_s] — espera a que la métrica supere la
# base. Sondear en vez de dormir un rato fijo: Prometheus raspa cada 15s, así
# que un sleep corto lee un valor viejo y la prueba falla sin que falle nada.
wait_metric() {
  local expr="$1" base="$2" limite="${3:-60}" v
  for _ in $(seq 1 $((limite/3))); do
    sleep 3
    v=$(metric "$expr")
    awk -v a="$base" -v b="$v" 'BEGIN{exit !(b>a)}' && { echo "$v"; return 0; }
  done
  echo "$v"; return 1
}

sql() { docker compose exec -T db psql -qtAX -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "$1"; }

# --------------------------------------------------------------------------
step "1. Idempotencia: una entrega duplicada no produce un segundo bundle"
# --------------------------------------------------------------------------
# Se toma el último trabajo YA convertido y se vuelve a publicar su mensaje,
# tal cual, en la cola principal. Es exactamente lo que pasa cuando RabbitMQ
# reentrega tras una caída del worker.
read -r JOB_ID FILE_ID OBJ NAME CTYPE SIZE <<<"$(sql "
  SELECT j.id, f.id, f.object_key, f.original_name, f.content_type, f.size
  FROM conversion_jobs j JOIN files f ON f.id = j.file_id
  WHERE j.status = 'converted' ORDER BY j.finished_at DESC LIMIT 1" | tr '|' ' ')"

if [ -z "${JOB_ID:-}" ]; then
  red "  No hay ningún trabajo convertido. Corre smoke.sh primero."
else
  echo "  trabajo: $JOB_ID (archivo $NAME)"
  before_skip=$(metric 'sum(convert_jobs_skipped_total)')
  before_out=$(sql "SELECT count(*) FROM file_outputs WHERE file_id = '$FILE_ID'")

  payload=$(python3 -c 'import json,sys; print(json.dumps({
    "JobID":sys.argv[1],"FileID":sys.argv[2],"ObjectKey":sys.argv[3],
    "ContentType":sys.argv[5],"OriginalName":sys.argv[4],"Size":int(sys.argv[6])}))' \
    "$JOB_ID" "$FILE_ID" "$OBJ" "$NAME" "$CTYPE" "$SIZE")

  curl -sS -u "$AUTH" -H 'Content-Type: application/json' \
    -d "$(python3 -c 'import json,sys; print(json.dumps({
      "properties":{"delivery_mode":2,"content_type":"application/json"},
      "routing_key":"file_conversion","payload":sys.argv[1],
      "payload_encoding":"string"}))' "$payload")" \
    "$RMQ/api/exchanges/%2F/amq.default/publish" | sed 's/^/  publicado: /'

  after_skip=$(wait_metric 'sum(convert_jobs_skipped_total)' "$before_skip" 60)
  after_out=$(sql "SELECT count(*) FROM file_outputs WHERE file_id = '$FILE_ID'")

  echo "  descartados por duplicado: $before_skip -> $after_skip"
  echo "  salidas del archivo:       $before_out -> $after_out"
  awk -v a="$before_skip" -v b="$after_skip" 'BEGIN{exit !(b>a)}' \
    && green "  ok    la entrega duplicada se descartó" \
    || red   "  FALLA no subió convert_jobs_skipped_total"
  [ "$before_out" = "$after_out" ] \
    && green "  ok    no se generó un segundo bundle" \
    || red   "  FALLA cambió el número de salidas"
fi

# --------------------------------------------------------------------------
step "2. Reintentos automáticos y cola de descartes"
# --------------------------------------------------------------------------
# Con MinIO caído el worker no puede leer el original: falla, reintenta
# WORKER_MAX_ATTEMPTS veces y al agotarlas manda el mensaje a .dead.
#
# No se espera un tiempo fijo: cada intento fallido tarda lo que tarde el
# cliente de S3 en rendirse, que es bastante más que el retardo entre
# reintentos. Se sondea la cola de descartes, que es la verdad de terreno, y
# MinIO no vuelve hasta que el trabajo haya terminado de agotarse.
dead_count() {
  docker compose exec -T rabbitmq rabbitmqctl list_queues --quiet name messages 2>/dev/null |
    awk '$1=="file_conversion.dead"{print $2}'
}

before_dead=$(dead_count); before_dead=${before_dead:-0}

echo "  reencolando el trabajo $JOB_ID con MinIO caído..."
docker compose stop minio >/dev/null 2>&1
sql "UPDATE conversion_jobs SET status='queued', attempts=0, error=NULL WHERE id='$JOB_ID'" >/dev/null
sql "UPDATE files SET status='ready' WHERE id='$FILE_ID'" >/dev/null
curl -sS -u "$AUTH" -H 'Content-Type: application/json' \
  -d "$(python3 -c 'import json,sys; print(json.dumps({
    "properties":{"delivery_mode":2,"content_type":"application/json"},
    "routing_key":"file_conversion","payload":sys.argv[1],
    "payload_encoding":"string"}))' "$payload")" \
  "$RMQ/api/exchanges/%2F/amq.default/publish" >/dev/null

LIMITE=${LIMITE:-300}
echo "  sondeando hasta ${LIMITE}s a que se agoten los $WORKER_MAX_ATTEMPTS intentos..."
after_dead=$before_dead
for i in $(seq 1 $((LIMITE/5))); do
  sleep 5
  after_dead=$(dead_count); after_dead=${after_dead:-0}
  intentos=$(sql "SELECT attempts FROM conversion_jobs WHERE id='$JOB_ID'")
  printf '\r  intentos: %s/%s   en .dead: %s   (%ss)   ' \
    "${intentos:-?}" "$WORKER_MAX_ATTEMPTS" "$after_dead" "$((i*5))"
  [ "$after_dead" -gt "$before_dead" ] 2>/dev/null && break
done
echo

if [ "${after_dead:-0}" -gt "${before_dead:-0}" ] 2>/dev/null; then
  green "  ok    el trabajo agotó sus intentos y acabó en file_conversion.dead"
else
  red "  FALLA nada llegó a .dead en ${LIMITE}s"
fi

echo "  estado final del trabajo: $(sql "SELECT status||' / intentos='||attempts FROM conversion_jobs WHERE id='$JOB_ID'")"
retried=$(metric 'sum(convert_jobs_retried_total)')
dead=$(metric 'sum(convert_jobs_dead_lettered_total)')
echo "  métricas (Prometheus, hasta 15s de retraso): reintentos=$retried descartados=$dead"

echo "  estado de las colas:"
docker compose exec -T rabbitmq rabbitmqctl list_queues --quiet name messages 2>/dev/null | sed 's/^/    /'

echo "  levantando MinIO de nuevo..."
docker compose start minio >/dev/null 2>&1
sleep 5

# --------------------------------------------------------------------------
step "3. Recuperación: lo descartado se convierte al volver el servicio"
# --------------------------------------------------------------------------
# Es lo mismo que hace el botón «Reintentar» de la interfaz: un trabajo en la
# cola de descartes no está perdido, solo está esperando a que se arregle la
# causa. Además deja el sistema en un estado en el que el paso 1 vuelve a
# tener un trabajo convertido con el que trabajar.
echo "  el archivo quedó en: $(sql "SELECT status FROM files WHERE id='$FILE_ID'")"
sql "UPDATE conversion_jobs SET status='queued', attempts=0, error=NULL WHERE id='$JOB_ID'" >/dev/null
sql "UPDATE files SET status='ready' WHERE id='$FILE_ID'" >/dev/null
curl -sS -u "$AUTH" -H 'Content-Type: application/json' \
  -d "$(python3 -c 'import json,sys; print(json.dumps({
    "properties":{"delivery_mode":2,"content_type":"application/json"},
    "routing_key":"file_conversion","payload":sys.argv[1],
    "payload_encoding":"string"}))' "$payload")" \
  "$RMQ/api/exchanges/%2F/amq.default/publish" >/dev/null

estado=""
for _ in $(seq 1 20); do
  sleep 3
  estado=$(sql "SELECT status FROM files WHERE id='$FILE_ID'")
  [ "$estado" = converted ] || [ "$estado" = failed ] && break
done
[ "$estado" = converted ] \
  && green "  ok    el trabajo se recuperó y volvió a convertirse" \
  || red   "  FALLA quedó en '$estado'"
salidas=$(sql "SELECT count(*) FROM file_outputs WHERE file_id = '$FILE_ID'")
echo "  salidas tras reconvertir: $salidas (la reconversión reemplaza, no acumula)"

# --------------------------------------------------------------------------
step "4. Escalado horizontal de workers"
# --------------------------------------------------------------------------
docker compose up -d --no-deps --scale worker=4 worker >/dev/null 2>&1
docker compose ps worker --format '{{.Name}}\t{{.Status}}' | sed 's/^/  /'
echo "  Prometheus descubre las réplicas por DNS; en Grafana debería verse"
echo "  subir «Conversiones en curso» al lanzar varias conversiones a la vez."
echo "  para volver:  make scale SERVICE=worker N=$WORKER_REPLICAS"
