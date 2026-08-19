# Plan de migración de la plataforma de reportes

Borrador de trabajo. Varias secciones remiten a documentos que todavía no se
han escrito, así que los enlaces que apuntan a ellos no resuelven: es
justamente el caso que la validación tiene que reportar sin bloquear la
publicación.

## Alcance

La migración cubre la generación de reportes programados y su distribución
por correo. Queda fuera el portal de consulta, que se aborda en una fase
posterior descrita en [el plan de fase dos](fase-dos.md).

## Inventario actual

Hay cuarenta y un reportes en producción. Veintinueve se generan de noche y
doce bajo demanda. El detalle por área está en la [matriz de
inventario](anexos/inventario.md), que sigue en revisión.

## Riesgos

El riesgo principal es la ventana nocturna: si la generación se pasa de las
seis de la mañana, los reportes llegan después de la apertura y pierden su
utilidad. La mitigación es paralelizar por área en vez de por reporte.

Un riesgo secundario es la dependencia de plantillas que nadie mantiene desde
hace tres años. El [registro de plantillas huérfanas](anexos/plantillas.md)
las lista, pero está incompleto.

## Cronograma

Doce semanas en cuatro fases de tres. Cada fase cierra con los reportes de un
área ya migrados y verificados contra la salida anterior, byte a byte cuando
el formato lo permite.

## Criterios de aceptación

La migración se da por terminada cuando los cuarenta y un reportes producen
salidas idénticas a las actuales durante dos semanas consecutivas, y la
ventana nocturna cierra antes de las cinco de la mañana.
