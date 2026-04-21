#!/bin/sh
set -e

/usr/local/bin/migrate
exec /usr/local/bin/clawguard
