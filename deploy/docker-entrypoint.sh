#!/bin/sh
# Entrypoint of the all-in-one image. With AWXNEXT_ADMIN_PASSWORD set, the
# superuser (AWXNEXT_ADMIN_USER, default admin) is created before the server
# starts, and its password is reset to that value on every start. A password
# the LOCAL_PASSWORD_MIN_* rules refuse is reported, and the server starts.
set -e
if [ "$1" = "serve" ] && [ -n "$AWXNEXT_ADMIN_PASSWORD" ]; then
	awxnext createsuperuser --username "${AWXNEXT_ADMIN_USER:-admin}" ||
		echo "awxnext: the superuser was not created or updated; starting anyway" >&2
fi
exec awxnext "$@"
