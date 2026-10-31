FROM node:22 AS ui
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
RUN npm run build

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /ui/dist ./ui/dist
# awxnext's version and build, shown in the UI's About dialog (release.yml
# sets them; .git is not copied, so there is no VCS stamp to fall back on).
ARG VERSION=dev
ARG COMMIT=
ARG BUILT=
RUN pkg=github.com/damhau/awxnext/internal/version \
	&& flags="-s -w -X $pkg.Version=$VERSION -X $pkg.Commit=$COMMIT -X $pkg.Built=$BUILT" \
	&& CGO_ENABLED=0 go build -trimpath -ldflags="$flags" -o /out/awxnext ./cmd/awxnext \
	&& CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/awxnext-jobexec ./cmd/awxnext-jobexec

# All-in-one: the control plane runs jobs itself with ansible-runner (no
# execution environment containers). Used by deploy/compose; good for small
# installs and for trying awxnext out.
FROM python:3.12-slim-bookworm AS all-in-one
COPY --from=ghcr.io/astral-sh/uv:0.13.5 /uv /usr/local/bin/uv
# PostgreSQL 16's client tools (pg_dump, pg_restore) for awxnext backup and
# restore, from the PostgreSQL project's repository: Debian's are version 15.
RUN apt-get update \
	&& apt-get install -y --no-install-recommends git openssh-client sshpass ca-certificates curl gnupg libcap2-bin \
	&& curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc | gpg --dearmor -o /usr/share/keyrings/pgdg.gpg \
	&& echo "deb [signed-by=/usr/share/keyrings/pgdg.gpg] http://apt.postgresql.org/pub/repos/apt bookworm-pgdg main" > /etc/apt/sources.list.d/pgdg.list \
	&& apt-get update \
	&& apt-get install -y --no-install-recommends postgresql-client-16 \
	&& apt-get purge -y curl gnupg && apt-get autoremove -y \
	&& rm -rf /var/lib/apt/lists/*
RUN uv pip install --system --no-cache ansible-core==2.20.10 ansible-runner==2.4.3
# /runner as in AWX's execution environments, so the image runs container
# group jobs too.
RUN useradd --uid 1000 --home-dir /var/lib/awxnext --create-home awx \
	&& mkdir -p /var/lib/awxnext/projects /var/lib/awxnext/jobs /runner \
	&& chown -R awx /var/lib/awxnext /runner \
	&& chmod 0711 /var/lib/awxnext && chmod 0700 /var/lib/awxnext/projects
# Each job runs as a job user of its own (internal/jobexec): awxjob0 to
# awxjob127, uids 50000 to 50127 (under 65536 for user namespace remapping),
# homes emptied before and after each job, and the helper that switches to
# them, run by the server's group only, with the file capabilities it needs
# and no others.
RUN for i in $(seq 0 127); do \
		echo "awxjob$i:x:$((50000 + i)):$((50000 + i))::/var/lib/awxnext/jobs/home/awxjob$i:/usr/sbin/nologin" >> /etc/passwd; \
		echo "awxjob$i:x:$((50000 + i)):" >> /etc/group; \
	done
COPY --from=build /out/awxnext-jobexec /usr/local/libexec/awxnext-jobexec
RUN chown root:awx /usr/local/libexec/awxnext-jobexec \
	&& chmod 0750 /usr/local/libexec/awxnext-jobexec \
	&& setcap cap_setuid,cap_setgid,cap_chown,cap_kill=ep /usr/local/libexec/awxnext-jobexec
COPY --from=build /out/awxnext /usr/local/bin/awxnext
COPY deploy/docker-entrypoint.sh /usr/local/bin/awxnext-entrypoint
COPY scripts/seed-demo.py /usr/local/bin/seed-demo
RUN chmod 755 /usr/local/bin/awxnext-entrypoint /usr/local/bin/seed-demo
ENV AWXNEXT_CONTAINER_RUNTIME=none \
	AWXNEXT_DEMO_REPO=/var/lib/awxnext/demo-repo \
	HOME=/var/lib/awxnext
USER awx
WORKDIR /var/lib/awxnext
VOLUME /var/lib/awxnext
EXPOSE 8052
ENTRYPOINT ["awxnext-entrypoint"]
CMD ["serve"]

# Default image: the binary only. Jobs run in execution environment
# containers (container groups on Kubernetes) or on remote agents.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/awxnext /usr/local/bin/awxnext
EXPOSE 8052
ENTRYPOINT ["/usr/local/bin/awxnext"]
CMD ["serve"]
