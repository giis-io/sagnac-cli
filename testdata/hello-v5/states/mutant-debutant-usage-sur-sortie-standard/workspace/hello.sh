#!/bin/sh
if [ $# -eq 0 ]; then
    echo "usage : ./hello.sh <prénom>"
    exit 64
fi
echo "Bonjour $1"
