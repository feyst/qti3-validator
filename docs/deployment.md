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

The memory of the process grows with the size of the documents being
validated at that moment, at most `MAX_CONCURRENT` at a time. With typical
QTI content, 256 MiB leaves ample headroom for the process. If you raise
`MAX_REQUEST_SIZE` or `MAX_FILE_SIZE` substantially, lower `MAX_CONCURRENT`
or raise the limit.

That figure excludes uploads, which are kept in `/tmp` (next section). If
`/tmp` is a tmpfs, its files count against the container's memory limit too.

Startup takes under a second, while the schemas of both QTI versions are
compiled; memory briefly peaks at about 100 MB.

## Temporary files

Every request body is written whole to `/tmp` before it is validated: up to
`MAX_PACKAGE_SIZE` (100 MiB) for a package, a multipart upload or a body
without an XML Content-Type, and up to `MAX_REQUEST_SIZE` (10 MiB) for a body
sent as `application/xml`. The file is deleted when the request finishes.

A body is received in full before the request waits for a validation slot,
so a slow upload holds no slot. That also means `MAX_CONCURRENT` does not
bound `/tmp`: it holds one file per request that is uploading or validating,
and the service itself does not limit connections. With the defaults,
that is 100 MiB per concurrent upload. An upload must complete within
`REQUEST_TIMEOUT` (60 s), or the request fails with 408; at 60 s, a 100 MiB
package needs about 14 Mbit/s.

Give `/tmp` disk, not memory:

- **Kubernetes:** an `emptyDir` without `medium: Memory`, as in the example
  below. Its `sizeLimit` caps the uploads; a pod that exceeds it is evicted.
- **Docker and Compose:** a volume, as in the examples below. `--tmpfs`
  would put uploads in the container's memory: three concurrent 100 MiB
  uploads are enough to get a 256 MiB container killed for running out of
  memory.

If `/tmp` must be a tmpfs, size it and the memory limit together: the
tmpfs size is the concurrent uploads you allow times `MAX_PACKAGE_SIZE`, and
the memory limit is that plus 256 MiB for the process. For example, with
`MAX_PACKAGE_SIZE=20971520` (20 MiB) and room for 8 uploads:
`--tmpfs /tmp:size=160m --memory 416m`. A full `/tmp` makes further uploads
fail with 500 instead of killing the container.

To bound the number of concurrent uploads, put the service behind a reverse
proxy that limits connections per client and in total, for example nginx's
`limit_conn`. nginx also buffers request bodies by default, so slow clients
are held by the proxy and the service receives each body at once.

## Hardening

The image contains only the service binary. It runs as an unprivileged user
and needs no network access. It writes only to `/tmp`, where an upload is
kept while it is validated. It can therefore run with a read-only root
filesystem, with a volume for `/tmp`:

```sh
docker run --rm -p 8080:8080 \
  --read-only \
  --mount type=volume,target=/tmp \
  --memory 256m \
  --stop-timeout 65 \
  ghcr.io/feyst/qti3-validator
```

On SIGTERM the service stops accepting requests and waits up to
`REQUEST_TIMEOUT` for the running ones. Give it that long before it is
killed: Docker waits 10 s by default, Compose 10 s, Kubernetes 30 s. The
examples allow 65 s for the default `REQUEST_TIMEOUT` of 60 s.

## Docker Compose

```yaml
services:
  qti-validator:
    image: ghcr.io/feyst/qti3-validator
    ports:
      - "8080:8080"
    read_only: true
    volumes:
      - type: volume
        target: /tmp
    mem_limit: 256m
    stop_grace_period: 65s
    restart: unless-stopped
```

The image has a `HEALTHCHECK`: the image contains no shell or `curl`, so the
binary checks `GET /health` itself with `/qti-validator -healthcheck`, on the
port of `ADDR`. `docker ps` and Compose show the container as healthy or
unhealthy; in Compose, `depends_on` with `condition: service_healthy` waits
for it. Kubernetes ignores it and uses the probes below.

## Kubernetes

```yaml
terminationGracePeriodSeconds: 65 # REQUEST_TIMEOUT plus 5 s
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
    emptyDir:
      sizeLimit: 2Gi # node disk, not memory: about 20 uploads of 100 MiB
```

## Logging

The service logs one JSON line per request to standard output, with method,
path, status, size and duration. Document contents are never logged. An
upload is deleted as soon as its request finishes.
