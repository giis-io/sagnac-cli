#!/bin/sh
if [ -z "$1" ]; then
    echo "hello.sh : il manque le prénom" >&2
    exit 1
fi
echo "Bonjour $1"
