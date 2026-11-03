#!/bin/sh
if [ "$#" -eq 0 ]; then
    echo "usage : hello.sh PRÉNOM" >&2
    exit 64
fi
echo "Bonjour $1"
[ -n "$HELLO_DEBUG" ] && echo "debug : prénom reçu ($1)" >&2
