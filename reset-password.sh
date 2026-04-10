#!/bin/bash
set -e

if [ -z "$1" ]; then
    echo "Usage: $0 <username>"
    echo "Example: $0 jdafoe"
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Load environment variables
set -a
. /etc/zbbs/env
set +a

"$SCRIPT_DIR/engine/zbbs" reset-password "$1"
