#!/bin/sh
if [ "$#" -eq 0 ]; then
    echo "usage : hello.sh PRÉNOM" >&2
    exit 64
fi
echo "Bonjour Awa"
