#!/bin/bash
# Detecta el nombre basándose en cómo Go renombró el archivo
NOMBRE_SERVICIO=$(basename "$0" .sh)

RUTA_LOG="${HOME}/${NOMBRE_SERVICIO}.log"

touch "$RUTA_LOG"

while true; do
    ULTIMO=$(tail -n 1 "$RUTA_LOG" 2>/dev/null | cut -d '-' -f 1 | tr -d ' ')
    [[ "$ULTIMO" =~ ^[0-9]+$ ]] || ULTIMO=0
    NUEVO=$((ULTIMO + 1))
    FECHA=$(date "+%Y-%m-%d %H:%M:%S")
    
    # stdbuf asegura que el Dashboard reciba la info en tiempo real
    echo "$NUEVO - $FECHA" | stdbuf -oL tee -a "$RUTA_LOG"
    sleep 2
done