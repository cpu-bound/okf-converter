# Documentos de ejemplo

Entradas listas para subir a la aplicación. Cubren los tres veredictos que la
validación puede emitir sobre un bundle publicable, para no depender de
improvisar un archivo durante una demostración.

| Archivo | Formato | Veredicto | Para qué sirve |
| --- | --- | --- | --- |
| `guia-despliegue.md` | Markdown | `valid` | Caso normal: seis secciones, encabezados anidados |
| `manual-api.html` | HTML | `valid` | Muestra que la segmentación se comporta igual en HTML |
| `plan-migracion.md` | Markdown | `valid_with_warnings` | Enlaces relativos a archivos que no existen |

Los tres producen seis unidades de conocimiento, así que sirven para comparar
la salida entre formatos.

`plan-migracion.md` es el que hace visible la clasificación del §5.2: enlaza a
tres documentos que no forman parte del bundle, lo que incumple la regla
`concept-links`. Esa regla es de severidad **advertencia** y no de error,
porque el cuerpo de un concepto arrastra el Markdown que el autor escribió: un
enlace a un archivo que no subió es un defecto que hay que reportarle, no una
razón para negarse a publicar su documento. El bundle se publica, se descarga,
y el veredicto y la bitácora dicen exactamente qué enlace falla y desde dónde.

Para probar el rechazo de formatos, cualquier `.zip` sirve: la API lo refuta
con `415` antes de emitir la URL de subida.
