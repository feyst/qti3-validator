# Running in production

## Memory and CPU

The service uses about 40 MB of memory when idle. Measured with the defaults
on 8 CPUs, the peak was about 100 MB, with 50 clients sending 1 MB packages
(74 files each) at the same time.

The image sets `GOMEMLIMIT=90MiB`, a soft limit for the Go runtime: the
garbage collector works harder as memory nears it. That keeps the peak at
about 100 MB instead of 110–130 MB, at no measurable cost in speed. If you
give the container more memory and send larger documents, raise it with
`-e GOMEMLIMIT=…`; it never makes the service fail, only collect sooner.

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
  ghcr.io/feyst/qti3-validator
```

## Docker Compose

```yaml
services:
  qti-validator:
    image: ghcr.io/feyst/qti3-validator
    ports:
      - "8080:8080"
    read_only: true
    tmpfs:
      - /tmp
    mem_limit: 256m
    restart: unless-stopped
```

The image has a `HEALTHCHECK`: the image contains no shell or `curl`, so the
binary checks `GET /health` itself with `/qti-validator -healthcheck`, on the
port of `ADDR`. `docker ps` and Compose show the container as healthy or
unhealthy; in Compose, `depends_on` with `condition: service_healthy` waits
for it. Kubernetes ignores it and uses the probes below.

## Kubernetes

```yaml
containers:
  - name: qti-validator
    image: ghcr.io/feyst/qti3-validator
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
