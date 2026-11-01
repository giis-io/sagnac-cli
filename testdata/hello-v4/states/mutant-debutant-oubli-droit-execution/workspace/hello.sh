#!/bin/sh
# Solution de référence du lab hello. Jamais visible de l'élève.
if [ "$#" -eq 0 ]; then
    echo "usage : hello.sh PRÉNOM" >&2
    exit 64
fi
printf 'Bonjour %s\n' "$1"
