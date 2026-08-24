# OKF Converter

OKF Converter es una aplicación web multiusuario que convierte documentos en
bundles de conocimiento en formato OKF. Un usuario se autentica y sube un
archivo. La API responde de inmediato con un identificador de trabajo y la
conversión ocurre en segundo plano, en workers independientes. El resultado
es un bundle: una carpeta autocontenida con un índice, una bitácora de la
conversión y un documento Markdown por cada unidad lógica del documento
original.

**Operación:** [Cómo usar](#cómo-usar) · [Comandos make](#comandos-make) ·
[Configuración](#configuración) · [Arquitectura](#arquitectura) ·
[Diagramas](#diagramas)

**Qué hace con los documentos:** [El bundle](#el-bundle) ·
[Formatos y segmentación](#formatos-de-entrada-y-segmentación) ·
[Validación](#validación-del-bundle) · [Descarga](#descarga-del-bundle) ·
[Dashboard](#el-dashboard) · [API](#la-api)

**Cómo se comporta bajo estrés:**
[Idempotencia y reintentos](#idempotencia-y-reintentos) ·
[Observabilidad](#observabilidad) ·
[Probar el stack levantado](#probar-el-stack-levantado)

## Cómo usar

Requiere Docker y Docker Compose.

```bash
make up
```

`make up` copia `.env.example` a `.env` si aún no existe y levanta el stack.
Sin `make`, son dos comandos:

```bash
cp .env.example .env
docker compose up --build
```

Una vez en ejecución:

- App: http://localhost:8080 (crear una cuenta y luego iniciar sesión para acceder a `/dashboard`)
- Consola de MinIO: http://localhost:9001 (`minioadmin` / `minioadminpassword`)
- Panel de administración de RabbitMQ: http://localhost:15672 (`okf` / `okf_password`)
- Prometheus: http://localhost:9090
- Grafana: http://localhost:3001 (`admin` / `admin`)

La API no publica ningún puerto en el host: se accede a través del proxy
inverso del frontend, en http://localhost:8080/api/.

### Qué subir

En [`ejemplos/`](ejemplos/) hay tres documentos listos para arrastrar a la
aplicación, uno por cada veredicto que la validación puede emitir:

| Archivo | Formato | Veredicto |
| --- | --- | --- |
| [`guia-despliegue.md`](ejemplos/guia-despliegue.md) | Markdown | `valid` |
| [`manual-api.html`](ejemplos/manual-api.html) | HTML | `valid` |
| [`plan-migracion.md`](ejemplos/plan-migracion.md) | Markdown | `valid_with_warnings` |

Los tres producen seis unidades de conocimiento, así que sirven para comparar
la salida entre formatos. Para ver el rechazo por formato vale cualquier
`.zip`. La API lo refuta con `415` antes de emitir la URL de subida.
[`ejemplos/README.md`](ejemplos/README.md) explica cada caso.

### Comprobar que funciona

Con el stack arriba, tres guiones lo ejercitan de punta a punta sin tocar la
interfaz. El detalle de cada uno está en
[Probar el stack levantado](#probar-el-stack-levantado).

```bash
make smoke        # ~20 s   flujo completo y aislamiento entre usuarios
make tolerancia   # ~3 min  idempotencia, reintentos y descartes
make carga        # ~11 min carga multiusuario con perfil de horas punta
```

### Volver a empezar

El esquema de Postgres se aplica solo en el primer arranque, y `init.sql`
solo corre sobre una base vacía. Si ya habías levantado el stack con una
versión anterior del esquema, hay que descartar los volúmenes:

```bash
make fresh          # equivale a make reset && make up
```

## Comandos make

`make help` lista todos los targets. Los más usados:

| Comando | Qué hace |
| --- | --- |
| `make up` | Levanta todo el stack en segundo plano |
| `make up-fg` | Igual, en primer plano y con logs |
| `make fresh` | Borra volúmenes y levanta el stack desde cero |
| `make down` / `make reset` | Detiene los contenedores / además borra los volúmenes |
| `make logs` / `make logs-one S=api` | Logs de todo / de un servicio |
| `make scale SERVICE=worker N=3` | Escala un servicio |
| `make queue` | Estado de las tres colas de conversión en RabbitMQ |
| `make psql` | Consola de PostgreSQL |
| `make urls` / `make config` | URLs y credenciales en uso / compose resuelto |
| `make test` | Pruebas de backend y frontend |
| `make check` | `go vet` + compilación + todas las pruebas |
| `make smoke` | Prueba de punta a punta contra el stack levantado |
| `make tolerancia` | Idempotencia, reintentos y descartes (detiene MinIO un rato) |
| `make carga` | Carga multiusuario con perfil de horas punta y escalado en caliente |

Los targets de pruebas y compilación corren dentro de contenedores, así que
no hace falta tener Go ni Node instalados en la máquina.

## Configuración

Toda la configuración (credenciales, puertos y endpoints) vive en el archivo
`.env` de la raíz, fuera del código y fuera de `docker-compose.yml`, que solo
describe el cableado entre servicios e interpola esas variables. `make urls`
imprime las direcciones y credenciales realmente en uso, y `make config`
muestra el compose ya resuelto.

Ese `.env` **no está versionado**. El repositorio trae
[`.env.example`](.env.example) con valores de desarrollo local, y `.env` sale
de copiarlo:

```bash
cp .env.example .env
```

Ningún valor de configuración está escrito en el código ni en el compose, así
que cambiar de entorno es cambiar ese archivo y nada más. Para un despliegue
real hay que editar `JWT_SECRET`, todas las contraseñas y poner
`APP_ENV=production`. Como `.env` está en `.gitignore`, esas credenciales no
pueden acabar en el repositorio por descuido.

`APP_ENV=production` activa el flag `Secure` de la cookie de sesión, que
exige servir la aplicación por HTTPS.

El backend en Go lee estas variables desde el entorno del proceso. La lista
completa, con sus valores por defecto, está en
`backend/internal/config/config.go`.

Las que gobiernan los workers:

| Variable | Por defecto | Qué controla |
| --- | --- | --- |
| `WORKER_REPLICAS` | `2` | Contenedores de worker (`make scale SERVICE=worker N=4`) |
| `WORKER_CONCURRENCY` | `2` | Trabajos en paralelo dentro de cada contenedor |
| `WORKER_MAX_ATTEMPTS` | `3` | Intentos antes de descartar. Con `1` no hay reintentos automáticos |
| `WORKER_RETRY_DELAY_SECONDS` | `10` | Espera entre intentos |

`WORKER_RETRY_DELAY_SECONDS` es un atributo de la cola `file_conversion.retry`,
así que cambiarlo solo surte efecto sobre una cola que todavía no exista. Hay
que borrarla, o `make reset`.

## Arquitectura

| Servicio     | Tecnología                         | Propósito                                     |
| ------------ | ----------------------------------- | ----------------------------------------------- |
| `frontend`   | Angular 21, servido con Nginx       | Páginas de autenticación + dashboard            |
| `api`        | Go                                   | API REST, autenticación, publica trabajos       |
| `worker`     | Go                                   | Consume trabajos y ejecuta la conversión        |
| `db`         | PostgreSQL 17                       | Usuarios, archivos, trabajos y validaciones     |
| `minio`      | MinIO                               | Documentos subidos y bundles generados          |
| `rabbitmq`   | RabbitMQ                            | Colas de trabajos: principal, reintentos y descartes |
| `prometheus` | Prometheus                          | Métricas de `api` y de cada réplica de `worker` |
| `grafana`    | Grafana                             | Dashboards sobre las métricas de Prometheus     |

Flujo de subida: el frontend pide al API una URL prefirmada, sube el archivo
directamente a MinIO y luego confirma la subida. Los bytes nunca pasan por el
API. El API encola un trabajo en RabbitMQ y responde de inmediato, sin
esperar a la conversión.

La descarga, en cambio, **sí** pasa por el API. Es el único punto donde se
puede exigir que quien pide sea el dueño del archivo y que el bundle haya
pasado la validación (ver [Descarga del bundle](#descarga-del-bundle)).

El navegador solo habla con `frontend`. Nginx sirve la aplicación compilada y
hace de proxy inverso hacia `api`, que no publica ningún puerto al host. El
`proxy_pass` apunta a una variable, no al nombre literal, porque así Nginx
resuelve el DNS en cada petición: con el nombre literal cachea la IP al
arrancar y se queda apuntando a una IP que Docker reasignó al recrear el
contenedor.

`api` y `worker` son dos binarios (`backend/cmd/api` y `backend/cmd/worker`)
del mismo módulo de Go, construidos en la misma imagen y separados a
propósito. El API solo publica en la cola y el worker solo consume. Comparten
la base de datos, el almacenamiento y la cola, pero nunca se comunican entre
sí. Por eso la conversión no puede ocupar una petición HTTP, y los workers se
escalan sin tocar el API.

```bash
make scale N=4          # docker compose up -d --scale worker=4
```

Cada contenedor de worker convierte `WORKER_CONCURRENCY` trabajos en
paralelo, así que el paralelismo total es réplicas × concurrencia. RabbitMQ
entrega cada trabajo a un solo consumidor, con `prefetch` acotado para que la
carga se reparta en vez de que un consumidor acapare la cola.

## Diagramas

### Arquitectura de contenedores

Cada bloque es un contenedor de `docker-compose.yml`. Las flechas indican
qué contenedores se comunican entre sí y con qué propósito.

```mermaid
flowchart LR
    Browser(["🌐 Navegador"])

    subgraph app ["Aplicación"]
        Frontend["frontend\nAngular + Nginx\n:8080"]
        API["api\nGo\nsolo publica"]
        Worker["worker × N\nGo\nsolo consume"]
    end

    subgraph data ["Datos y almacenamiento"]
        DB[("db\nPostgreSQL 17")]
        Minio[("minio\nMinIO")]
        Rabbit["rabbitmq\nRabbitMQ"]
    end

    subgraph obs ["Observabilidad"]
        Prom["prometheus"]
        Grafana["grafana\n:3001"]
    end

    Browser -- "HTTP :8080" --> Frontend
    Frontend -- "proxy /api/*" --> API
    Browser -- "sube con URL prefirmada\n(los bytes no pasan por el API)" --> Minio

    API -- "SQL: usuarios, archivos,\ntrabajos, validaciones" --> DB
    API -- "firma URLs de subida,\nlee el .zip para servirlo" --> Minio
    API -- "publica trabajos" --> Rabbit

    Rabbit -- "un trabajo por worker" --> Worker
    Worker -- "SQL: reclamo del trabajo,\nestado, validación" --> DB
    Worker -- "lee el original,\nescribe el bundle" --> Minio
    Worker -- "maneja intentos fallidos" --> Rabbit

    Prom -- "raspa /metrics" --> API
    Prom -- "descubre las réplicas\npor DNS y raspa /metrics" --> Worker
    Grafana -- "consulta métricas" --> Prom
```

### Flujo de datos y proceso de decisión

Desde que un usuario sube un archivo hasta que puede descargar el bundle,
incluyendo las decisiones que toma el sistema en cada paso (validación de
tamaño, formato del documento, tamaño de los bloques, validación del bundle
generado y resultado final de la conversión).

```mermaid
flowchart TD
    A(["Usuario selecciona un archivo"]) --> B["Frontend pide una URL de subida prefirmada\nPOST /api/files/upload-url"]
    B --> C["API crea el registro del archivo en estado 'pending'\ny firma una URL de subida hacia MinIO"]
    C --> D["El navegador sube el archivo\ndirectamente a MinIO"]
    D --> E["Frontend confirma la subida\nPOST /api/files/:id/confirm"]
    E --> F{"¿El tamaño subido\ncoincide con el declarado?"}
    F -- No --> F1(["Se elimina el objeto y el registro\nError 409"])
    F -- Sí --> G["Archivo pasa a 'ready'\nse encola un trabajo en RabbitMQ"]
    G --> H(["API responde de inmediato,\nsin URL de descarga: todavía no hay bundle"])
    G -. async .-> I["Un worker (contenedor aparte)\nconsume el trabajo de RabbitMQ"]
    I --> I1{"¿Logra reclamar el trabajo?\n(transición atómica en Postgres)"}
    I1 -- No --> I2(["Entrega duplicada o trabajo ya hecho:\nack y nada más, un solo efecto final"])
    I1 -- Sí --> J["Archivo pasa a 'converting'\nse descarga el objeto original desde MinIO"]
    J --> K{"¿Qué formato tiene?"}
    K -- ".md" --> L1["Encabezados '#' y subrayados"]
    K -- ".html" --> L2["Se renderiza como Markdown:\nh1-h6 → encabezados"]
    K -- ".txt / .pdf" --> L3["Encabezados, secciones numeradas\ny palabras clave (Capítulo, Sección…)"]
    K -- ".csv" --> L4["Cada fila es una unidad"]
    L1 --> M{"¿Se detectó\nestructura?"}
    L2 --> M
    L3 --> M
    M -- Sí --> M1["Una unidad por sección del nivel más alto\nque de verdad divide el documento.\nLas subsecciones quedan dentro de la suya"]
    M -- No --> N{"¿Documento\nbreve?"}
    N -- Sí --> N1["Todo el documento\nes un único concepto"]
    N -- No --> N2["Se divide por párrafos, cortando bloques\nde más de 20.000 bytes sin romper UTF-8"]
    M1 --> V
    N1 --> V
    N2 --> V
    L4 --> V
    V["Se arma el bundle: index.md con los enlaces\nen orden, log.md con la bitácora,\ny un .md por unidad"]
    V --> W["Se valida el bundle todavía en memoria:\nestructura mínima y enlaces (plataforma)\n+ frontmatter y campo 'type' (OKF)"]
    W --> W1["Se guarda el veredicto y el informe\nregla por regla en la BD, pase o no"]
    W1 --> X{"¿Clasificación?"}
    X -- "inválido" --> S
    X -- "válido / válido con advertencias" --> O["Cada archivo del bundle se guarda\ncomo objeto en MinIO y se registra en la BD"]
    O --> P["Se empaqueta el bundle en un .zip\ny se guarda en su clave determinista"]
    P --> Q{"¿La conversión tuvo éxito?"}
    Q -- Sí --> R(["Archivo 'converted': el bundle está publicado"])
    Q -- No --> S(["Archivo 'failed'\nno se publicó ningún bundle"])
    S --> S1{"¿Quedan intentos?"}
    S1 -- Sí --> S2["Trabajo vuelve a 'queued' y archivo a 'ready'.\nEl mensaje espera en file_conversion.retry"]
    S2 -. vence el TTL .-> I
    S1 -- No --> S3(["El mensaje va a file_conversion.dead.\nEl archivo queda 'failed' con el motivo"])
    R --> U["El frontend sondea GET /api/files hasta que\nno queda nada en 'ready' ni 'converting',\ny habilita la descarga"]
    U --> U0["El usuario descarga por la API\nGET /api/files/:id/bundle"]
    U0 --> U1
    U1{"¿Es el dueño\ny está publicado?"}
    U1 -- Sí --> U2(["La API hace stream del .zip\ndesde MinIO"])
    U1 -- No --> U3(["404 si no es suyo,\n409 con el motivo si no está publicado"])
    S3 --> T["El usuario puede pedir un reintento manual\nPOST /api/files/:id/retry, que crea un trabajo nuevo"]
    T -.-> I
```

## El bundle

```
notas-de-clase/
├── index.md              navegación y datos del bundle
├── log.md                trazabilidad de la conversión
├── 01-introduccion.md    un documento por unidad lógica,
├── 02-metodologia.md     numerado en el orden del original
└── 03-conclusiones.md
```

`index.md` enumera y enlaza los conceptos en el orden del documento de
origen, junto con los datos del bundle (documento original, tipo, tamaño,
número de unidades y trabajo que lo produjo). `log.md` registra, con marca de
tiempo, cada operación de la conversión y una tabla de las unidades
detectadas con su procedencia. Cada concepto abre con su título, lleva el
contenido de su unidad y cierra con enlaces al índice y a sus vecinos, de
modo que el bundle se puede recorrer completo desde cualquier archivo.

Todos los archivos llevan frontmatter YAML, como pide
[Open Knowledge Format v0.1](https://cloud.google.com/blog/products/data-analytics/how-the-open-knowledge-format-can-improve-data-sharing),
que define un bundle como un directorio de Markdown con frontmatter y exige
exactamente un campo en cada concepto: `type`. Se rellenan además los campos
estándar consultables que el generador puede conocer:

```yaml
---
type: "concept"
title: "Introducción"
description: "Unidad 1 de 3 del documento «notas-de-clase.md»."
tags: ["okf", "notas-de-clase"]
source: "notas-de-clase.md"
timestamp: 2026-08-15T18:42:04Z
---
```

`index.md` y `log.md` son nombres reservados por la especificación y llevan
`type: index` y `type: log`. Los conceptos se enlazan entre sí con enlaces
Markdown normales, que es la forma en que OKF representa el grafo de
relaciones.

Un resultado exitoso nunca es un Markdown suelto: o se generan todos los
archivos del bundle, o el trabajo falla. Un documento breve sin divisiones
produce un bundle igual de válido, con `index.md`, `log.md` y un único
concepto.

Los archivos del bundle se guardan además como objetos individuales en MinIO,
así que se puede leer `index.md` sin descargar el paquete completo, y el
bundle entero se empaqueta como un `.zip` con una sola carpeta raíz.

## Formatos de entrada y segmentación

Una unidad lógica es una sección del documento, no un párrafo arbitrario. La
segmentación parte de la estructura del propio documento:

| Formato | Cómo se detecta la estructura |
| --- | --- |
| Markdown (`.md`) | Encabezados ATX (`#`) y subrayados setext. Lo que está dentro de un bloque de código no cuenta |
| HTML (`.html`, `.htm`) | Se renderiza a Markdown (`<h1>` a `<h6>` pasan a encabezados) y se segmenta igual. `<script>`, `<style>` y `<head>` se descartan |
| Texto plano (`.txt`) | Encabezados Markdown, secciones numeradas (`3.1 Metodología`, `1. Introducción`) y palabras clave (`Capítulo IV`, `Sección 2`) |
| PDF (`.pdf`) | Se extrae el texto reconstruyendo los espacios por posición de carácter y se trata como texto plano |
| CSV (`.csv`) | Cada fila es una unidad. Ninguna detección de encabezados encontraría sus divisiones reales |

Dos decisiones:

- **Se segmenta por el nivel más alto que divide el documento.** Un
  informe cuyo único encabezado de nivel 1 es su propio título se divide por
  sus secciones de nivel 2, no se deja entero. Las subsecciones quedan dentro
  de la sección a la que pertenecen.
- **Sin estructura, un documento breve es un solo concepto**, que es
  exactamente lo que pide el enunciado para el caso del documento corto sin
  divisiones. Solo un documento sin estructura y demasiado grande para eso
  (más de 20.000 bytes) cae al respaldo de dividir por párrafos.

El formato se valida en la recepción: una subida que el pipeline no podría
convertir se rechaza con `415` antes de firmar la URL, en vez de fallar en un
worker minutos después.

## Validación del bundle

Ningún bundle se publica sin haber sido validado. La comprobación ocurre
mientras el bundle todavía está en memoria, así que un bundle inválido nunca
llega al almacenamiento de objetos y nunca se ofrece para descarga.

Se miden dos cosas por separado:

- **Validez de plataforma.** ¿Es un bundle usable?
- **Conformidad OKF.** ¿Cumple la especificación Open Knowledge Format v0.1?

| Regla | Ámbito | Severidad |
| --- | --- | --- |
| Contiene `index.md`, `log.md` y al menos un concepto | plataforma | error |
| Todos los enlaces de `index.md` resuelven dentro del bundle | plataforma | error |
| Todos los conceptos son alcanzables desde `index.md` | plataforma | error |
| Los enlaces relativos dentro de los conceptos resuelven | plataforma | advertencia |
| Todos los archivos abren con frontmatter YAML | OKF | error |
| Todos los archivos declaran el campo obligatorio `type` | OKF | error |
| Todos los archivos traen los campos estándar consultables | OKF | advertencia |
| `index.md` y `log.md` declaran el tipo de su rol | OKF | advertencia |

El resultado se clasifica en **válido**, **válido con advertencias** o
**inválido**, y se guarda en la tabla `files` (`validation` y el informe regla
por regla en `validation_report`). Un bundle inválido deja el archivo en
`failed` con el motivo. Uno con advertencias se publica igual y las reporta.

Que una regla sea *advertencia* y no *error* tiene una razón concreta: el
cuerpo de un concepto arrastra el Markdown del documento original, así que un
enlace relativo que el autor escribió hacia un archivo que no subió es un
defecto real que hay que reportar, pero no uno por el que valga la pena negarse
a entregar el bundle.

El veredicto completo queda también dentro del propio bundle, en la sección
`## Validación` de `log.md`, con todas las reglas, las superadas incluidas,
que es lo que pide el §3 del enunciado sobre trazabilidad.

## Descarga del bundle

`GET /api/files/{id}/bundle` entrega el `.zip`. La descarga pasa por la API y
no por una URL prefirmada, porque es el único punto donde se pueden exigir las
dos condiciones del §6:

- **que quien pide sea el dueño del archivo.** Una URL prefirmada, una vez
  entregada, le responde a quien la tenga.
- **que el bundle esté publicado**, es decir, que haya pasado la validación.

La API no carga el archivo en memoria: lo transmite por flujo desde MinIO, así
que un bundle grande le cuesta un buffer y no su tamaño completo.

| Situación | Respuesta |
| --- | --- |
| Bundle publicado | `200` con el `.zip` |
| El archivo es de otro usuario | `404` (no `403`: si un id existe o no, no es asunto de quien pregunta) |
| Todavía convirtiendo | `409` con el motivo |
| Conversión fallida o bundle inválido | `409` explicando cuál de las dos |

Los archivos individuales del bundle (`GET /api/files/{id}/outputs`) están
tras la misma puerta: un bundle que no se publicó tampoco se ofrece por
partes.

## El dashboard

Tras iniciar sesión, el usuario ve **únicamente sus propios archivos**: el
listado se acota por `user_id` en la consulta, no filtrando después, así que
un error en un handler no puede ensancharlo a los archivos de otro.

El seguimiento del estado es por sondeo (`GET /api/files`), una sola petición
por vuelta para todos los archivos que se estén siguiendo. Dos detalles
deliberados:

- **Solo se sondea si hay algo que esperar.** Un archivo en `ready` o
  `converting` está esperando a un worker. Uno en `pending` es una subida que
  nunca se confirmó y que ningún worker va a tomar, así que no cuenta.
- **El sondeo se rinde a los 10 minutos**, para que un worker caído no deje
  una pestaña preguntando indefinidamente. Queda el botón «Actualizar».

El botón de descarga solo se habilita con el bundle publicado. El veredicto de
la validación se muestra junto al estado, y al abrirlo se pide el informe
regla por regla a `GET /api/files/{id}`. Ese informe no viaja en el listado
justamente porque el listado se sondea cada pocos segundos.

El selector de archivo lleva un `accept` con los formatos soportados, así que
el rechazo por formato ocurre antes incluso de la petición.

| Estado | Etiqueta |
| --- | --- |
| `pending` | Subida sin confirmar |
| `ready` | En cola |
| `converting` | Convirtiendo |
| `converted` | Bundle publicado |
| `failed` | Falló (con botón de reintento) |

## La API

Todas las rutas de `/api/files` exigen sesión y están acotadas al usuario
autenticado: el `user_id` va en la consulta SQL, no en un filtro posterior.

| Método | Ruta | Qué hace |
| --- | --- | --- |
| `POST` | `/api/auth/register` | Crea la cuenta y abre sesión |
| `POST` | `/api/auth/login` | Inicia sesión (cookie httpOnly con el JWT) |
| `POST` | `/api/auth/logout` | Cierra la sesión |
| `GET` | `/api/auth/me` | Usuario de la sesión actual |
| `POST` | `/api/auth/check-email` | Comprueba si un correo ya está registrado |
| `POST` | `/api/auth/reset-password` | Restablece la contraseña |
| `GET` | `/api/files` | Los archivos del usuario, del más reciente al más antiguo |
| `POST` | `/api/files/upload-url` | Registra el archivo y firma la URL de subida. `415` si el formato no se admite |
| `POST` | `/api/files/{id}/confirm` | Verifica la subida y encola la conversión |
| `GET` | `/api/files/{id}` | Estado del archivo + informe de validación regla por regla |
| `POST` | `/api/files/{id}/retry` | Reintento manual. Idempotente: repetirlo no encola dos veces |
| `GET` | `/api/files/{id}/outputs` | Archivos del bundle por separado, con URLs prefirmadas |
| `GET` | `/api/files/{id}/bundle` | Descarga el `.zip` por flujo |
| `GET` | `/api/health` | Sonda de salud |
| `GET` | `/metrics` | Métricas de Prometheus (también en cada worker) |

## Idempotencia y reintentos

### Una entrega duplicada no convierte dos veces

Antes de convertir, el worker **reclama el trabajo** con una transición
atómica en Postgres. Gana un solo reclamante. Cualquier otra entrega hace
`ack` y no toca el convertidor. Es lo que exige el §6: un único efecto final
y, a lo sumo, un bundle publicado.

| Estado del trabajo | ¿Reclamable? | Por qué |
| --- | --- | --- |
| `queued` | Sí | Trabajo nuevo, o devuelto a la espera para otro intento |
| `failed` | Sí | El intento anterior terminó y perdió. Reintentar es el objetivo |
| `converting` | Solo si viene marcado como reentrega | RabbitMQ solo reentrega cuando el canal del consumidor anterior ya no existe, así que es un trabajo cuyo worker murió a mitad. Negarlo dejaría el archivo varado |
| `converted` | Nunca | Publicar un segundo bundle es justo lo que el §6 prohíbe |

La tercera fila evita tener que elegir entre idempotencia y recuperación:
**un doble `publish` no viene marcado como reentrega**, así que sigue quedando
en un solo efecto.

### Reintentos automáticos acotados

```
file_conversion         trabajos por convertir
file_conversion.retry   espera entre intentos, vence hacia la principal
file_conversion.dead    intentos agotados, para inspección
```

`file_conversion.retry` no tiene consumidor. Lleva un TTL y devuelve por
dead-letter a la principal cuando vence, que es la forma estándar de retrasar
una reentrega en RabbitMQ sin plugins. El TTL es atributo de la cola, no de
cada mensaje: un TTL por mensaje solo expira los de la cabeza, y una espera
larga bloquearía las cortas de atrás. El costo es que la espera es fija.

Al agotar `WORKER_MAX_ATTEMPTS` el mensaje va a descartes y el archivo queda
en `failed` con el motivo. Sobre ese estado actúa el **reintento manual** del
§5.2, que crea un trabajo nuevo enlazado al anterior por `retry_of`. Entre
intento e intento, en cambio, el trabajo vuelve a `queued` y el archivo a
`ready`: si no, el dashboard vería `failed`, lo daría por terminal y dejaría
de sondear un trabajo que sigue en proceso.

`make queue` muestra las tres colas. Las métricas están en
[Observabilidad](#observabilidad).

### Si el broker se cae

Un canal AMQP abierto al arrancar solo sirve hasta el primer corte de red.
Después todo falla con `channel/connection is not open` y nada lo repara, con
el proceso en pie y su health en verde. Por eso ni el API ni los workers
guardan un canal fijo: piden uno sano en cada uso y lo reabren cuando el
anterior murió, redeclarando las colas. Los workers vuelven a suscribirse con
espera creciente hasta 30 s.

Si aun así no se pudo encolar, la API responde `503` y marca el archivo como
`failed`, el único estado desde el que el dashboard ofrece reintentar.

## Observabilidad

Prometheus raspa `/metrics` del API y de **cada réplica de worker**, que
descubre por DNS de Docker (`dns_sd_configs`), así que escalar los workers no
exige tocar la configuración de Prometheus. Grafana trae aprovisionado el
dashboard *Conversión de documentos*
([`observability/grafana/dashboards/jobs.json`](observability/grafana/dashboards/jobs.json)).

| Métrica | Quién la mueve | Para qué |
| --- | --- | --- |
| `convert_jobs_enqueued_total` | API | Trabajos publicados en la cola |
| `convert_jobs_processed_total{status}` | Workers | Intentos de conversión por resultado |
| `convert_jobs_in_flight` | Workers | Cuánto del pool está ocupado, y hace visible el escalado |
| `convert_job_duration_seconds` | Workers | Duración de la conversión (p50/p95/p99) |
| `convert_bundles_validated_total{verdict}` | Workers | Bundles válidos / con advertencias / inválidos |
| `convert_jobs_skipped_total` | Workers | Entregas duplicadas descartadas |
| `convert_jobs_retried_total` | Workers | Intentos fallidos reprogramados |
| `convert_jobs_dead_lettered_total{reason}` | Workers | Mensajes descartados definitivamente |
| `rabbitmq_detailed_queue_messages{queue}` | RabbitMQ | Profundidad real de cada cola |

Están separadas porque significan cosas distintas. Un documento que **no pasa
la validación** y un trabajo que **se cae** son fallas de naturaleza muy
diferente, y en un único contador de fallos no se distinguirían.
`convert_jobs_skipped_total` contando hacia arriba no es un problema: cada
unidad es una conversión repetida que **no** ocurrió.

Que la API y los workers expongan las suyas por separado es lo que hace
visible la diferencia entre lo encolado y lo procesado.

El trabajo pendiente, en cambio, **no** se deriva restando contadores: viven
en cada réplica de worker, así que al reiniciar una vuelven a cero y la resta
se va a negativo. Es un valor instantáneo, no un acumulado, y quien lo conoce
es RabbitMQ, que publica la profundidad de cada cola como gauge en el puerto
15692.

## Probar el stack levantado

Las pruebas unitarias (`make test`) corren contra dobles y no tocan la
infraestructura. Lo que hay que demostrar solo se ve con todo levantado: que
el trabajo se convierte una sola vez, que un fallo se reintenta y acaba
descartado, y que un usuario no ve los archivos de otro. Para eso hay tres
guiones en `scripts/`, y los tres pasan por la API igual que lo haría el
navegador.

```bash
make up
make smoke        # ~20 s
make tolerancia   # ~3 min: detiene MinIO a propósito
make carga        # ~11 min: ventana de 10 min, escala los workers y los restaura
```

Los tres sondean en vez de esperar un tiempo fijo. Prometheus raspa cada 15 s
y un intento fallido tarda lo que tarde el cliente de S3 en rendirse, bastante
más que el retardo entre reintentos. Con un `sleep` fijo la prueba falla sin
que falle nada.

`make smoke` registra dos cuentas, rechaza un `.zip` con 415, sube un `.md`,
espera la conversión, descarga el `.zip` y verifica por dentro `index.md`,
`log.md`, los conceptos, el frontmatter y la validación de la bitácora. Cierra
con el aislamiento: la segunda cuenta no ve nada en `GET /api/files`, y el
detalle y la descarga de un id ajeno dan `404`.

`make tolerancia` cubre lo que no se ve desde la interfaz. Republica un
mensaje ya procesado y comprueba las dos cosas a la vez: que suba
`convert_jobs_skipped_total` **y** que no aparezca un segundo bundle. Luego
detiene MinIO y sondea hasta que el trabajo agota sus intentos y cae en
`file_conversion.dead`. Al restaurar MinIO verifica que se recupera, que es lo
que hace el botón «Reintentar».

### La prueba de carga

`make carga` mide lo que el resto del README afirma en prosa: que el API solo
publica en la cola, que el pendiente se acumula en RabbitMQ y no dentro de una
petición HTTP, y que escalar workers absorbe una hora punta sin tocar el API.

**No es una ráfaga.** Ocho cuentas suben documentos durante diez minutos con
forma de horas punta, y sondean `GET /api/files` cada 2,5 s como haría su
navegador. En el valle entre los dos picos el guion escala los workers de 2 a
6 y al terminar restaura `WORKER_REPLICAS`.

```
   8.3 |               #                         #
   5.9 |              ###                       ###
   3.6 |             ####                       ###
   1.2 |  #         ######           #         #####        #
   0.0 +----------------------------------------------------------
                  pico 1          ^         pico 2
       doc/s en cubos de 10 s   (^ = momento del escalado)
```

Es parametrizable: `VENTANA=900 PICO=15 ESCALA=8 make carga`, y
`VENTANA=120 make carga` sirve de humo del propio guion en tres minutos.

Tres decisiones lo hacen medible en vez de anecdótico:

- **Las llegadas se sortean como un proceso de Poisson**, no a intervalos
  regulares. Un goteo perfectamente espaciado a la misma media no llenaría la
  cola nunca.
- **Los dos picos son el mismo sorteo**, no solo la misma distribución. Así lo
  único que separa un pico del otro son los workers que había detrás.
- **La cuenta de «no se perdió nada» se le hace a Postgres, no a Prometheus.**
  Los contadores viven dentro de cada réplica de worker y desaparecen con las
  que el guion destruye al restaurar la escala. Es la misma razón por la que
  el dashboard no deriva el pendiente restando contadores (ver
  [Observabilidad](#observabilidad)).

El reporte enfrenta los dos picos y cierra con lo que vio el cliente
(percentiles de cada llamada) frente a lo que dice el servidor. La diferencia
entre ambos **es** el respaldo de la cola. Incluye el **desfase**, lo que se
retrasó cada subida respecto a su hora asignada: si crece, el cuello de
botella está en el guion y el perfil medido fue más plano que el planeado.

Falla si algún `confirm` no da `200`, si un archivo acaba en `failed`, si algo
llega a `.dead`, si la cola no se drena o si un documento encolado no figura
convertido en Postgres. Deja los archivos sin borrar a propósito, para poder
mirar el dashboard de Grafana después. `make reset` lo limpia.

## Estructura del proyecto

```
backend/         Backend en Go (un módulo, dos binarios)
  cmd/api/        Punto de entrada del API: publica trabajos, nunca convierte
  cmd/worker/     Punto de entrada del worker: consume trabajos y convierte
  internal/
    auth/           Registro, login, JWT, hashing de contraseñas
    bundle/         Construcción y validación del bundle OKF
    config/         Carga de la configuración desde el entorno
    convert/        Colas, extracción de texto, segmentación, pipeline
    db/             Pool de conexiones a Postgres
    files/          Handlers de subida, estado, listado, descarga y reintento
    httpx/          Respuestas JSON y CORS
    metrics/        Métricas de Prometheus
    middleware/     Guard de autenticación, recuperación de panics
    storage/        Wrapper del cliente de MinIO
frontend/        App de Angular (login/registro/dashboard)
database/        SQL de inicialización de Postgres
observability/   Configuración de Prometheus y dashboards/provisioning de Grafana
scripts/         Pruebas contra el stack levantado (smoke, tolerancia, carga)
ejemplos/        Documentos de entrada listos para probar y demostrar
.env.example     Plantilla de configuración, se copia a .env (que no se versiona)
```

## Desarrollo del backend

El API es un módulo estándar de Go en `backend/`.

```bash
cd backend
go build ./...
go test ./...
```

Para ejecutarlo fuera de Docker se necesita tener Postgres, MinIO y RabbitMQ
accesibles, además de las variables de entorno definidas en
`docker-compose.yml` (`DATABASE_URL`, `RABBITMQ_URL`, `JWT_SECRET`,
`MINIO_*`, etc.). `backend/internal/config/config.go` tiene la lista completa
y sus valores por defecto.

## Desarrollo del frontend

Angular 21 requiere Node 20 o superior.

```bash
cd frontend
npm ci
npm start        # ng serve, http://localhost:4200
npm test         # pruebas unitarias con Vitest
```

`ng serve` redirige `/api/*` hacia `http://localhost:8080`
([`proxy.conf.json`](frontend/proxy.conf.json)), que es el frontend en Docker,
y ese a su vez hace de proxy inverso hacia el API. Para desarrollar contra el
backend basta con tener el stack levantado (`make up`). **El API no publica
ningún puerto en el host y no hace falta que lo publique.**

La sesión sigue funcionando entre los dos puertos porque la cookie se emite
sin `Domain`, y las cookies de `localhost` no distinguen el puerto.
