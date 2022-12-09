#!/bin/bash

set -e

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

SRC_DEST="${SCRIPT_DIR}/.src/"
# sync all sources for dlv
rm -Rf "${SRC_DEST}"
mkdir "${SRC_DEST}"
for d in cmd pkg vendor clients
do
    cp -Rf "${SCRIPT_DIR}/../../../${d}/" "${SRC_DEST}/${d}/"
done

# build loki -gcflags "all=-N -l" disables optimizations that allow for better run with combination with Delve debugger.
CGO_ENABLED=0 GOOS=linux go build -mod=vendor -gcflags "all=-N -l" -o "${SCRIPT_DIR}/loki" "${SCRIPT_DIR}/../../../cmd/loki"
# ## install loki driver to send logs
docker plugin install grafana/loki-docker-driver:main-c44fb2e --alias loki-compose --grant-all-permissions || true
docker-compose -f "${SCRIPT_DIR}"/docker-compose.yml up --build "$@"
