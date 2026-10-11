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

### What a document takes

To check a document against the rules, the service builds a tree of it in
memory: one node for every element, attribute, piece of text, comment and
processing instruction. A node takes some 170 bytes, and the indexes the
rules use up to 80 more per element. So it is the number of nodes that
decides what a document takes, not its size: a megabyte of prose is some
60,000 nodes and takes 13 MB, a megabyte of nothing but `<b/>x` is 420,000
nodes and takes 84 MB.

Measured for documents of one kind of content each, per MiB of document:

| Content | Nodes per MiB | Memory per MiB | Largest document in 256 MiB | in 512 MiB | in 1 GiB |
| --- | --- | --- | --- | --- | --- |
| One long text, or attributes with long values | few | 6 MiB | 10 MiB¹ | 10 MiB¹ | 10 MiB¹ |
| Response declarations and response processing | 44,000 | 11 MiB | 10 MiB¹ | 10 MiB¹ | 10 MiB¹ |
| Choices of an interaction | 53,000 | 12 MiB | 10 MiB¹ | 10 MiB¹ | 10 MiB¹ |
| Prose: paragraphs of a sentence each | 62,000 | 15 MiB | 10 MiB¹ | 10 MiB¹ | 10 MiB¹ |
| MathML: short formulas | 85,000 | 21 MiB | 8.9 MiB | 10 MiB¹ | 10 MiB¹ |
| Deeply nested `<div>` | 95,000 | 25 MiB | 7.7 MiB | 10 MiB¹ | 10 MiB¹ |
| Empty comments, `<!---->` | 150,000 | 29 MiB | 6.6 MiB | 10 MiB¹ | 10 MiB¹ |
| Empty MathML elements, `<mi/>` | 210,000 | 53 MiB | 3.6 MiB | 8.5 MiB | 10 MiB¹ |
| Empty paragraphs, `<p/>` | 262,000 | 65 MiB | 2.9 MiB | 6.8 MiB | 10 MiB¹ |
| An element and a character, `<b/>x` | 419,000 | 91 MiB | 2.1 MiB | 4.9 MiB | 10 MiB¹ |

¹ `MAX_REQUEST_SIZE`; memory would allow more.

"Memory per MiB" is what the service reserves, which is 1.1 to 1.4 times
what the validation really takes. The last four kinds are not what QTI
content looks like; they are what a document made to take as much memory as
possible looks like.

### How the service sizes itself

The service bounds the memory of the validations in progress, not only their
number:

- **`MAX_CONCURRENT`** (default: the number of CPUs) bounds how many
  validations run at once: the CPU bound.
- **`MAX_VALIDATION_MEMORY`** bounds what they hold in memory together: the
  memory bound.

Requests beyond `MAX_CONCURRENT` wait for a slot in about the order they
came, at most `MAX_QUEUE` of them (by default 4 × `MAX_CONCURRENT`); beyond
that a request gets 503 at once, with `Retry-After`. A waiting request that
gets no slot within `REQUEST_TIMEOUT` gets 503 too.

A validation reserves its memory in two steps. To read a document and check
it against the XML Schema, it reserves 1 MiB plus six times the size of the
document; the schema check reads the document as a stream and keeps little.
Before it builds the tree for the rules, it counts the nodes of the document
in one quick pass (some 10 ms per MiB) and reserves what they take. In a
package every file does so for itself, one after the other.

A validation that finds no room waits for others to finish, as a request
waits for a free slot: at most `MAX_QUEUE` requests wait in all, and one that
gets no memory within `REQUEST_TIMEOUT` gets 503 with `Retry-After`. While it
waits it gives up its slot, so small documents are validated beside a large
one that runs, instead of behind a large one that waits. Two large documents
that do not fit together are validated one after the other; neither is slowed
down by the other, as they were when both were let in and the garbage
collector had to fight for the last bytes.

Only a document that could never fit, with nothing else running, is refused.
The answer is 413 with code `too_large` and the numbers:

```json
{
  "code": "too_large",
  "message": "the document has 2.6 million nodes and needs about 654 MiB of memory to check; this service allows 192 MiB for one document (MAX_VALIDATION_MEMORY)"
}
```

It is refused as soon as it is read, before the schema check. In a package
this is an error of that file, with the same code and message; the other
files are validated as usual.

`MAX_VALIDATION_MEMORY` is derived at startup from the container's memory
limit (cgroup v2 or v1): the limit, less 64 MiB for the process itself, less
the upload spool when `/tmp` is a tmpfs (see
[Temporary files](#temporary-files)). In a 256 MiB container with a volume on
`/tmp` it is 192 MiB, which the service logs at startup. When no memory limit
can be read it is 1 GiB, and the service logs a warning: set
`MAX_VALIDATION_MEMORY`, or a memory limit, on a host with less to spare.

### Sizing the container

With 256 MiB the service validates any QTI content up to `MAX_REQUEST_SIZE`
(10 MiB) that is made of text, interactions and declarations, and refuses
documents of more than some 750,000 nodes. To take every document up to
`MAX_REQUEST_SIZE` whatever it is made of, give the container 1 GiB. The
levers:

- **More memory:** the budget follows the container's limit, and with it both
  the largest document and how many validations run beside each other.
- **`MAX_REQUEST_SIZE` and `MAX_FILE_SIZE`:** the largest document and the
  largest file of a package. Lower them to what you expect to receive: a
  smaller document reserves less to be read.
- **`MAX_VALIDATION_MEMORY`:** set it to give the validations less than the
  derived budget, on a host shared with other work, or more, at the risk of
  running out of memory.
- **`MAX_CONCURRENT`** stays the CPU bound. Raising it does not risk memory:
  the memory bound holds whatever it is.

The image sets `GOMEMLIMIT=90MiB`, a soft limit for the Go runtime: the
garbage collector works harder as memory nears it, which keeps the memory
of a service that validates typical content at about 100 MB instead of
110–130 MB. It never makes the service fail. When `GOMEMLIMIT` is empty, the
service derives the soft limit from the container's memory limit at startup
(the limit less 64 MiB) and logs it: run the image with `-e GOMEMLIMIT=` in
a larger container that validates large documents, so the collector does not
work against a limit the documents exceed anyway. A value you set is kept.

These figures exclude uploads, which are kept in `/tmp` (see
[Temporary files](#temporary-files)). If `/tmp` is a tmpfs, its files count
against the container's memory limit too, and the service takes them off the
budget of the validations.

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
