# Running in production

## Running the container

```sh
docker run -d -p 8080:8080 \
  --mount type=volume,target=/tmp \
  --memory 256m \
  --stop-timeout 65 \
  ghcr.io/feyst/qti3-validator
```

The volume holds uploads while they are validated (see
[Temporary files](#temporary-files)).

On SIGTERM the service stops accepting requests and waits up to
`REQUEST_TIMEOUT` for the running ones. Give it that long before it is
killed: Docker waits 10 s by default, Compose 10 s, Kubernetes 30 s. The
examples allow 65 s for the default `REQUEST_TIMEOUT` of 60 s.

## Memory and CPU

The service uses about 30 MB of memory when idle. Measured with the defaults
on 8 CPUs, the peak was about 100 MB, with 50 clients sending 1 MB packages
(74 files each) at the same time.

### How the service sizes itself

Validating a document means building an in-memory tree of it: one node per
element, attribute and piece of text, about 250 bytes each. The memory a
validation needs therefore grows with the number of nodes in the document,
which for dense markup can be one node per a few bytes. Measured peak heap
for a 10 MiB document ranges from about 13× its size for typical QTI content
to about 110× for pathological single-character markup. A single 10 MiB
document of the densest kind needs close to a gigabyte and cannot be
validated in a 256 MiB container at all; two ordinary large documents at
once already fill one.

So the service bounds the memory of the validations in flight, not only their
number:

- **`MAX_CONCURRENT`** (default: the number of CPUs) bounds how many
  validations run at once — the CPU bound.
- **`MAX_VALIDATION_MEMORY`** bounds the heap they may hold at once — the
  memory bound. Each validation reserves an estimate of its peak heap before
  it starts (the document size, or for a package the largest entry it may
  hold, times a worst-case bytes-per-byte factor). A validation that does not
  fit waits for memory to free up, exactly as it waits for a free slot: in
  about the order requests came, at most `MAX_QUEUE` waiting, then 503 with
  `Retry-After`, and 503 again if none frees within `REQUEST_TIMEOUT`. So two
  large documents never validate at the same time when together they would
  exceed the budget, while small documents still run `MAX_CONCURRENT` at a
  time.

`MAX_VALIDATION_MEMORY` is derived at startup from the container's memory
limit (cgroup v2 or v1): the limit, less headroom for the process, less the
upload spool when `/tmp` is a tmpfs (see [Temporary files](#temporary-files)).
In a 256 MiB container with a disk volume on `/tmp` the budget is about
208 MiB, which the service logs at startup. When no memory limit can be read
the budget falls back to a constant and the service says so; set
`MAX_VALIDATION_MEMORY` explicitly on a memory-constrained host without a
readable limit.

A single document whose estimate exceeds the whole budget can never be
validated safely, so it is refused at once with **413** rather than run and
risk an out-of-memory kill. The largest document a container can validate is
roughly `MAX_VALIDATION_MEMORY` ÷ the worst-case factor: about 1.5 MiB in a
256 MiB container, about 4 MiB in 512 MiB, about 8 MiB in 1 GiB. A package is
never refused for this reason; instead its per-entry size is capped to what
fits, so an entry over that size is reported as too large and the rest of the
package is still validated.

### Tuning for larger documents and larger containers

The budget, and with it the largest document that validates and how many run
at once, scales with the container's memory, so the usual lever is to give
the container more memory. To validate documents up to `MAX_REQUEST_SIZE`
(10 MiB by default) of any shape, allow roughly 1.3 GiB; for typical content,
far less. The other levers:

- **`MAX_REQUEST_SIZE` / `MAX_FILE_SIZE`** cap the size of a single document
  or package entry. Lowering them to the largest input you actually expect
  both hardens the service and lets more validations share the budget.
- **`MAX_VALIDATION_MEMORY`** can be set explicitly to override the derived
  default — higher if you have measured that your content is less dense than
  the worst case and want larger documents admitted, lower to leave more of a
  shared host to other work.
- **`MAX_CONCURRENT`** stays the CPU bound. Raising it no longer risks memory:
  the memory bound holds whatever it is.

The image sets `GOMEMLIMIT=90MiB`, a soft limit for the Go runtime: the
garbage collector works harder as memory nears it. That keeps the peak at
about 100 MB instead of 110–130 MB, at no measurable cost in speed. When
`GOMEMLIMIT` is **not** set, the service derives a soft limit from the
container's memory limit at startup (the limit less headroom), so a larger
container collects less often; run the image with `-e GOMEMLIMIT=` (empty) to
opt into that instead of the image's 90 MiB. Setting `GOMEMLIMIT` to a value
keeps that value; it never makes the service fail, only collect sooner.

That memory excludes uploads, which are kept in `/tmp` (see
[Temporary files](#temporary-files)). If `/tmp` is a tmpfs, its files count
against the container's memory limit too, and against the validation-memory
budget.

Startup takes under a second, while the schemas of both QTI versions are
compiled; memory briefly peaks at about 100 MB.

## Temporary files

Every request body is written whole to `/tmp` before it is validated: up to
`MAX_PACKAGE_SIZE` (100 MiB) for a package, a multipart upload or a body
without an XML Content-Type, and up to `MAX_REQUEST_SIZE` (10 MiB) for a body
sent as `application/xml`. The file is deleted when the request finishes.

A body is received in full before the request waits for a validation slot,
so a slow upload holds no slot. An upload must complete within
`REQUEST_TIMEOUT` (60 s), or the request fails with 408; at 60 s, a 100 MiB
package needs about 14 Mbit/s.

`MAX_SPOOL_SIZE` bounds the bytes in `/tmp` across all requests; by default
it is 4 × `MAX_PACKAGE_SIZE`, 400 MiB. A request reserves its share as its
body arrives, so an upload holds budget only for the bytes it has sent:
stalled uploads do not lock others out, however many there are. An upload
whose next bytes do not fit gets 503 at once, with `Retry-After`. The
reservation is freed when the request finishes. A body that could never fit gets 413 instead: a Content-Length
above the limit of its kind or above `MAX_SPOOL_SIZE` at once, a chunked
body as soon as it grows past either. Give `/tmp` at least
`MAX_SPOOL_SIZE`.

Give `/tmp` disk, not memory:

- **Kubernetes:** an `emptyDir` without `medium: Memory`, as in the example
  below. Its `sizeLimit` caps the uploads; a pod that exceeds it is evicted.
- **Docker and Compose:** a volume, as in the examples. `--tmpfs` puts
  uploads in the container's memory.

If `/tmp` is a tmpfs, the service lowers `MAX_SPOOL_SIZE` at startup to the
size of the tmpfs and, when the container has a memory limit (cgroup v2 or
v1), to that limit minus 160 MiB for the process, and logs a warning with
the numbers. In a 256 MiB container that leaves 96 MiB: one package at a
time, and a package above 96 MiB gets 413. If less than `MAX_REQUEST_SIZE`
is left, the service logs an error: it runs, but refuses larger uploads. A
full budget refuses uploads instead of getting the container killed for
running out of memory.

When `/tmp` is a tmpfs **and** no memory limit can be read, the spool cannot
be clamped to the container's memory at all: it falls back to `min(400 MiB,
tmpfs size)`, and the service logs a warning at startup. Give `/tmp` a
size-limited tmpfs or a disk volume, or set a memory limit, so uploads kept
in memory cannot exhaust it.

To use a tmpfs anyway, size it, `MAX_SPOOL_SIZE` and the memory limit
together: the tmpfs and `MAX_SPOOL_SIZE` are the concurrent uploads you
allow times `MAX_PACKAGE_SIZE`, and the memory limit is that plus 256 MiB for
the process. For example, with 20 MiB packages and room for 8 uploads:

```sh
docker run -d -p 8080:8080 \
  -e MAX_PACKAGE_SIZE=20971520 -e MAX_SPOOL_SIZE=167772160 \
  --tmpfs /tmp:size=160m --memory 416m \
  --stop-timeout 65 \
  ghcr.io/feyst/qti3-validator
```

## Public deployments

The service bounds its own work: `MAX_CONCURRENT` validations, `MAX_QUEUE`
waiting requests and `MAX_SPOOL_SIZE` bytes of uploads, each refused with
503 when full. It does not bound connections or tell clients apart: one
client can take all of it, and others then get 503.

Exposed to the internet, put it behind a reverse proxy that limits
connections per client and in total. For example, with nginx:

```nginx
limit_conn_zone $binary_remote_addr zone=perclient:10m;
limit_conn_zone $server_name zone=total:1m;

server {
    location /api/validate {
        limit_conn perclient 2;
        limit_conn total 32;
        client_max_body_size 100m;  # MAX_PACKAGE_SIZE
        proxy_request_buffering on; # the default
        proxy_pass http://qti-validator:8080;
    }
}
```

nginx buffers each request body before it passes it on, so slow clients are
held by the proxy, and the service receives each body at once.

Some risk remains: `MAX_CONCURRENT` requests whose documents take the most
work can hold every slot for up to `REQUEST_TIMEOUT`, and validation of other
requests waits until then. Rate limits per client (`limit_req`) make that
harder; a lower `REQUEST_TIMEOUT`, such as `15s`, shortens it, at the cost of
`too_complex` for the largest legitimate packages.

## Hardening

The image contains only the service binary. It runs as an unprivileged user
and needs no network access. It writes only to `/tmp`, where an upload is
kept while it is validated. It can therefore run with a read-only root
filesystem, with a volume for `/tmp`:

```sh
docker run -d -p 8080:8080 \
  --read-only \
  --mount type=volume,target=/tmp \
  --memory 256m \
  --stop-timeout 65 \
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
      sizeLimit: 1Gi # node disk, not memory; more than MAX_SPOOL_SIZE (400 MiB)
```

## Logging

The service logs one JSON line per request to standard output, with method,
path, status, size and duration. Document contents are never logged. An
upload is deleted as soon as its request finishes.
