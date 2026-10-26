#!/bin/sh
# Start a throwaway local PostgreSQL for development and tests (no Docker needed).
set -e
PGBIN=${PGBIN:-$(ls -d /usr/lib/postgresql/*/bin | sort -V | tail -1)}
DATA=${PGDATA_DIR:-/var/lib/postgresql/awxdev}
if [ "$(id -u)" = 0 ]; then RUN="su postgres -c"; else RUN="sh -c"; fi
[ -d "$DATA/data" ] || $RUN "$PGBIN/initdb -D $DATA/data -A trust -U postgres" >/dev/null
$RUN "$PGBIN/pg_ctl -D $DATA/data -l $DATA/log -o '-k $DATA -p 5432 -c listen_addresses=localhost -c fsync=off' status" >/dev/null 2>&1 ||
	$RUN "$PGBIN/pg_ctl -D $DATA/data -l $DATA/log -o '-k $DATA -p 5432 -c listen_addresses=localhost -c fsync=off' start"
