#!/bin/env bash
curl -i -N -H "Connection: Upgrade" -H "Upgrade: websocket" -H "Host: example.com:80" -H "Origin: http://www.websocket.org" -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: SGVsbG8sIHdvcmxkIQ==" -H "X-Scope-OrgID: 1" --data-urlencode 'query={compose_project="loki-boltdb-storage-s3"}' -G http://localhost:8004/loki/api/v1/read

