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
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/awxnext ./cmd/awxnext

# All-in-one: the control plane runs jobs itself with ansible-runner (no
# execution environment containers). Used by deploy/compose; good for small
# installs and for trying awxnext out.
FROM python:3.12-slim-bookworm AS all-in-one
COPY --from=ghcr.io/astral-sh/uv:0.13.5 /uv /usr/local/bin/uv
# PostgreSQL 16's client tools (pg_dump, pg_restore) for awxnext backup and
# restore, from the PostgreSQL project's repository: Debian's are version 15.
RUN apt-get update \
	&& apt-get install -y --no-install-recommends git openssh-client sshpass ca-certificates curl gnupg \
	&& curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc | gpg --dearmor -o /usr/share/keyrings/pgdg.gpg \
	&& echo "deb [signed-by=/usr/share/keyrings/pgdg.gpg] http://apt.postgresql.org/pub/repos/apt bookworm-pgdg main" > /etc/apt/sources.list.d/pgdg.list \
	&& apt-get update \
	&& apt-get install -y --no-install-recommends postgresql-client-16 \
	&& apt-get purge -y curl gnupg && apt-get autoremove -y \
	&& rm -rf /var/lib/apt/lists/*
RUN uv pip install --system --no-cache ansible-core==2.20.10 ansible-runner==2.4.3
RUN useradd --uid 1000 --home-dir /var/lib/awxnext --create-home awx \
	&& mkdir -p /var/lib/awxnext/projects /var/lib/awxnext/jobs \
	&& chown -R awx /var/lib/awxnext
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
