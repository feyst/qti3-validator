# Running in production

## Memory and CPU

The service uses about 40 MB of memory when idle. Measured with the defaults
on 8 CPUs, the peak was 115–120 MB, with 50 clients sending 1 MB packages
(74 files each) at the same time.

Memory grows with the size of the documents being validated at that moment,
times `MAX_CONCURRENT`. With typical QTI content, a memory limit of 256 MiB
leaves ample headroom. If you raise `MAX_REQUEST_SIZE` or `MAX_FILE_SIZE`
substantially, lower `MAX_CONCURRENT` or raise the limit.

Startup takes under a second, while the schemas of both QTI versions are
compiled; memory briefly peaks at about 100 MB.

## Hardening

The image contains only the service binary. It runs as an unprivileged user
and needs no network access. It writes only to `/tmp`, where an uploaded
package is kept while it is validated. It can therefore run with a read-only
root filesystem:

```sh
docker run --rm -p 8080:8080 \
  --read-only \
  --tmpfs /tmp \
  --memory 256m \
  kennisnet/qti3-validator
```

## Docker Compose

```yaml
services:
  qti-validator:
    image: kennisnet/qti3-validator
    ports:
      - "8080:8080"
    read_only: true
    tmpfs:
      - /tmp
    mem_limit: 256m
    restart: unless-stopped
```

The image contains no shell or `curl`, so a Docker `HEALTHCHECK` inside the
container is not possible. Check `GET /health` from your orchestrator or load
balancer instead.

## Kubernetes

```yaml
containers:
  - name: qti-validator
    image: kennisnet/qti3-validator
    ports:
      - containerPort: 8080
    resources:
      requests:
        memory: 64Mi
        cpu: 100m
      limits:
        memory: 256Mi
    securityContext:
      readOnlyRootFilesystem: true
      runAsNonRoot: true
      allowPrivilegeEscalation: false
    volumeMounts:
      - name: tmp
        mountPath: /tmp
    readinessProbe:
      httpGet:
        path: /health
        port: 8080
    livenessProbe:
      httpGet:
        path: /health
        port: 8080
volumes:
  - name: tmp
    emptyDir: {}
```

## Logging

The service logs one JSON line per request to standard output, with method,
path, status, size and duration. Document contents are never logged. An
uploaded package is deleted as soon as its request finishes.
