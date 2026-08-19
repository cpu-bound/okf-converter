# Guía de despliegue de servicios en contenedores

Documento de referencia para equipos que publican servicios internos.
Cubre desde la preparación de la imagen hasta la observación en producción.

## Preparación de la imagen

Una imagen de producción debe ser reproducible: la misma etiqueta tiene que
producir siempre el mismo binario. Eso significa fijar la versión base en vez
de apoyarse en `latest`, y compilar dentro de la propia imagen en lugar de
copiar un artefacto construido en la máquina de alguien.

### Compilación en varias etapas

La etapa de compilación trae el compilador y las dependencias; la etapa final
solo copia el binario resultante. La imagen que se publica no contiene el
código fuente ni las herramientas de construcción, y suele quedar en unas
decenas de megabytes en lugar de varios cientos.

### Usuario sin privilegios

El proceso no necesita ser root para escuchar en un puerto por encima de
1024. Declarar un usuario propio evita que una vulnerabilidad en la
aplicación se convierta en control total del contenedor.

## Configuración por entorno

La configuración no se hornea en la imagen. La misma imagen debe poder correr
en desarrollo y en producción, y lo único que cambia son las variables de
entorno con las que se lanza.

Las credenciales nunca van en el repositorio. Los valores de desarrollo sí
pueden versionarse, porque no protegen nada y permiten que un clon limpio
arranque sin pasos previos; los de producción se inyectan desde el gestor de
secretos del entorno.

## Salud y arranque ordenado

Un servicio que depende de una base de datos no puede asumir que la base ya
está lista cuando el contenedor arranca. Declarar una comprobación de salud
permite que el orquestador espere a que la dependencia responda de verdad,
en vez de esperar un número fijo de segundos y cruzar los dedos.

La comprobación debe ejercitar el camino real. Un endpoint que devuelve
siempre 200 sin consultar nada no informa de nada.

## Escalado horizontal

Los procesos que consumen trabajo de una cola escalan sumando réplicas. Para
que eso funcione, el trabajo tiene que ser idempotente: si el mismo mensaje
llega dos veces, el efecto debe ser el mismo que si hubiera llegado una.

La forma habitual de conseguirlo es reclamar el trabajo antes de ejecutarlo,
con una operación atómica sobre la base de datos. El que gana el reclamo
convierte; el que lo pierde descarta el mensaje y sigue.

## Observación

Un servicio sin métricas es una caja negra. Como mínimo conviene exponer el
volumen de trabajo procesado, su tasa de fallo y su duración.

Conviene separar las métricas por naturaleza del fallo. Un documento que no
pasa una validación y un proceso que se cae son problemas muy distintos, y
un único contador de errores los mezcla hasta volverlos inútiles.
