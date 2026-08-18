#!/usr/bin/env bash
# Prueba de punta a punta del stack de OKF Converter.
#
# Recorre el §6 del enunciado con dos cuentas distintas: subida, conversión
# asíncrona, validación, descarga del bundle, aislamiento entre usuarios y
# rechazo de formatos. No toca la base de datos ni las colas por dentro: todo
# pasa por la API, igual que lo haría el navegador.
#
#   bash smoke.sh            # contra http://localhost:8080
#   BASE=http://otro:9999 bash smoke.sh
set -uo pipefail

BASE="${BASE:-http://localhost:8080}"
WORK="$(mktemp -d)"
STAMP="$(date +%s)"
PASS=0
FAIL=0

green() { printf '\033[32m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }
step()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }

ok()   { PASS=$((PASS+1)); green "  ok    $*"; }
bad()  { FAIL=$((FAIL+1)); red   "  FALLA $*"; }

# check <descripción> <esperado> <obtenido>
check() {
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (esperaba '$2', obtuve '$3')"; fi
}

# api <cookiejar> <método> <ruta> [cuerpo json]
# Deja el código HTTP en $STATUS y el cuerpo en $BODY. No imprime nada: si se
# llamara dentro de $( ) correría en una subshell y $STATUS no volvería.
STATUS=""
BODY=""
api() {
  local jar="$1" method="$2" path="$3" body="${4:-}"
  if [ -n "$body" ]; then
    STATUS=$(curl -sS -b "$jar" -c "$jar" -X "$method" "$BASE$path" \
      -H 'Content-Type: application/json' -d "$body" \
      -o "$WORK/resp.json" -w '%{http_code}')
  else
    STATUS=$(curl -sS -b "$jar" -c "$jar" -X "$method" "$BASE$path" \
      -o "$WORK/resp.json" -w '%{http_code}')
  fi
  BODY=$(cat "$WORK/resp.json")
}

# jq_ <expresión sobre d> — evalúa contra el último $BODY
jq_() { printf '%s' "$BODY" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1],{},{"d":d}))' "$1"; }

# --------------------------------------------------------------------------
step "0. La API responde"
# --------------------------------------------------------------------------
api "$WORK/none.jar" GET /api/health
check "GET /api/health" 200 "$STATUS"
[ "$STATUS" = 200 ] || { red "El stack no está arriba. Corre 'make up' primero."; exit 1; }

# --------------------------------------------------------------------------
step "1. Dos cuentas independientes"
# --------------------------------------------------------------------------
A_MAIL="ana-$STAMP@example.com"
B_MAIL="beto-$STAMP@example.com"
JAR_A="$WORK/a.jar"; JAR_B="$WORK/b.jar"

api "$JAR_A" POST /api/auth/register \
  "{\"name\":\"Ana\",\"email\":\"$A_MAIL\",\"password\":\"contrasena-larga\"}"
check "registro de Ana" 201 "$STATUS"
api "$JAR_B" POST /api/auth/register \
  "{\"name\":\"Beto\",\"email\":\"$B_MAIL\",\"password\":\"contrasena-larga\"}"
check "registro de Beto" 201 "$STATUS"

api "$WORK/anon.jar" GET /api/files
check "sin sesión, GET /api/files rechaza" 401 "$STATUS"

# --------------------------------------------------------------------------
step "2. Formato no soportado"
# --------------------------------------------------------------------------
api "$JAR_A" POST /api/files/upload-url \
  '{"filename":"cosas.zip","contentType":"application/zip","size":1024}'
check "un .zip se rechaza en la puerta" 415 "$STATUS"

# --------------------------------------------------------------------------
step "3. Ana sube un documento"
# --------------------------------------------------------------------------
DOC="$WORK/manual.md"
cat > "$DOC" <<'DOCEOF'
# Manual de operación

Documento de prueba para el bundle OKF.

## Puesta en marcha

Levantar el stack con un solo comando y esperar a que la API responda.

### Requisitos previos

Solo Docker. Ni Go ni Node hacen falta en la máquina.

## Resolución de problemas

Si un trabajo falla, el worker lo reintenta un número acotado de veces.

## Anexo [borrador]

Un encabezado con corchetes, para ejercitar el escape de los enlaces.
DOCEOF
SIZE=$(wc -c < "$DOC")

api "$JAR_A" POST /api/files/upload-url \
  "{\"filename\":\"manual.md\",\"contentType\":\"text/markdown\",\"size\":$SIZE}"
check "se pide la URL prefirmada" 200 "$STATUS"
FILE_ID=$(jq_ 'd["file"]["id"]')
PUT_URL=$(jq_ 'd["uploadUrl"]')

code=$(curl -sS -o /dev/null -w '%{http_code}' -X PUT "$PUT_URL" \
  -H 'Content-Type: text/markdown' --data-binary @"$DOC")
check "subida directa a MinIO" 200 "$code"

api "$JAR_A" POST "/api/files/$FILE_ID/confirm"
check "confirmación (encola el trabajo)" 200 "$STATUS"

# --------------------------------------------------------------------------
step "4. Conversión asíncrona"
# --------------------------------------------------------------------------
STATE=""
for i in $(seq 1 60); do
  api "$JAR_A" GET "/api/files/$FILE_ID"
  STATE=$(jq_ 'd["file"]["status"]')
  [ "$STATE" = converted ] || [ "$STATE" = failed ] && break
  sleep 2
done
check "el archivo termina convertido" converted "$STATE"
VERDICT=$(jq_ 'd["file"].get("validation")')
echo "  veredicto de validación: $VERDICT"
case "$VERDICT" in
  valid|valid_with_warnings) ok "veredicto publicable" ;;
  *) bad "veredicto inesperado: $VERDICT" ;;
esac

# --------------------------------------------------------------------------
step "5. Descarga del bundle"
# --------------------------------------------------------------------------
ZIP="$WORK/bundle.zip"
code=$(curl -sS -b "$JAR_A" -o "$ZIP" -w '%{http_code}' "$BASE/api/files/$FILE_ID/bundle")
check "GET /api/files/{id}/bundle" 200 "$code"
if [ "$code" = 200 ]; then
  echo "  contenido del zip:"
  unzip -l "$ZIP" | sed 's/^/    /'
  unzip -o -q "$ZIP" -d "$WORK/out"
  ROOT=$(find "$WORK/out" -mindepth 1 -maxdepth 1 -type d | head -1)
  [ -f "$ROOT/index.md" ] && ok "index.md presente" || bad "falta index.md"
  [ -f "$ROOT/log.md" ]   && ok "log.md presente"   || bad "falta log.md"
  n=$(find "$ROOT" -name '*.md' ! -name index.md ! -name log.md | wc -l)
  [ "$n" -ge 3 ] && ok "$n conceptos generados" || bad "solo $n conceptos"
  grep -q '^type:' "$ROOT"/*.md && ok "frontmatter con 'type'" || bad "falta 'type' en el frontmatter"
  grep -q '## Validación' "$ROOT/log.md" && ok "log.md trae la validación" || bad "log.md sin sección de validación"
fi

# --------------------------------------------------------------------------
step "6. Aislamiento entre usuarios"
# --------------------------------------------------------------------------
api "$JAR_B" GET /api/files
check "Beto lista sus archivos" 200 "$STATUS"
n=$(jq_ 'len(d["files"])')
check "Beto no ve nada de Ana" 0 "$n"

api "$JAR_B" GET "/api/files/$FILE_ID"
check "Beto no puede consultar el archivo de Ana" 404 "$STATUS"
code=$(curl -sS -b "$JAR_B" -o /dev/null -w '%{http_code}' "$BASE/api/files/$FILE_ID/bundle")
check "Beto no puede descargar el bundle de Ana" 404 "$code"

api "$JAR_A" GET /api/files
n=$(jq_ 'len(d["files"])')
check "Ana sí ve su archivo" 1 "$n"

# --------------------------------------------------------------------------
printf '\n\033[1m== Resumen\033[0m\n'
green "  $PASS comprobaciones ok"
[ "$FAIL" -gt 0 ] && red "  $FAIL fallas"
echo "  artefactos en: $WORK"
exit $(( FAIL > 0 ))
