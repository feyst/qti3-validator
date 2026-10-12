# Running in production

## Running the container

```sh
docker run -d -p 8080:8080 \
  --mount type=volume,target=/tmp \
  --memory 256m \
  --stop-timeout 65 \
  --restart unless-stopped \
  ghcr.io/feyst/qti3-validator
```

The volume holds the uploads of 256 KiB and more while they are validated
(see [Temporary files](#temporary-files)). `--restart unless-stopped` starts
the service again if it ever ends by itself; why it ended is in its log (see
[Logging](#logging)).

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
rules use up to 80 more per element and 45 more per attribute. So it is the
number of nodes that
decides what a document takes, not its size: a megabyte of prose is some
60,000 nodes and takes 13 MB, a megabyte of nothing but `<b/>x` is 420,000
nodes and takes 84 MB.

Measured for documents of one kind of content each, per MiB of document:

| Content | Nodes per MiB | Memory per MiB | Largest document in 256 MiB | in 512 MiB | in 1 GiB |
| --- | --- | --- | --- | --- | --- |
| One long text, or attributes with long values | few | 6 MiB | 10 MiB¹ | 10 MiB¹ | 10 MiB¹ |
| Response declarations and response processing | 44,000 | 12 MiB | 10 MiB¹ | 10 MiB¹ | 10 MiB¹ |
| Choices of an interaction, with their text | 53,000 | 13 MiB | 10 MiB¹ | 10 MiB¹ | 10 MiB¹ |
| Prose: paragraphs of a sentence each | 62,000 | 15 MiB | 10 MiB¹ | 10 MiB¹ | 10 MiB¹ |
| Hottexts or gaps of nothing but an identifier | 64,000–73,000 | 17–19 MiB | 9.3 MiB | 10 MiB¹ | 10 MiB¹ |
| MathML: short formulas | 85,000 | 21 MiB | 8.1 MiB | 10 MiB¹ | 10 MiB¹ |
| Deeply nested `<div>` | 95,000 | 25 MiB | 7.1 MiB | 10 MiB¹ | 10 MiB¹ |
| Empty comments, `<!---->` | 150,000 | 29 MiB | 6.1 MiB | 10 MiB¹ | 10 MiB¹ |
| Empty MathML elements, `<mi/>` | 210,000 | 53 MiB | 3.3 MiB | 8.2 MiB | 10 MiB¹ |
| Empty paragraphs, `<p/>` | 262,000 | 65 MiB | 2.7 MiB | 6.6 MiB | 10 MiB¹ |
| An element and a character, `<b/>x` | 419,000 | 91 MiB | 1.9 MiB | 4.7 MiB | 10 MiB¹ |

¹ `MAX_REQUEST_SIZE`; memory would allow more.

The sizes are for 2 CPUs: one document may take 176 MiB, 432 MiB and 944 MiB
in these containers (see below). A document counts for what its rules would
take when it is read, whether or not it then passes its schema.

"Memory per MiB" is what the service reserves, which is 1.1 to 1.5 times
what the validation really takes: never less, which tests hold it to for
some sixty kinds of content. The last four kinds are not what QTI content
looks like; they are what a document made to take as much memory as
possible looks like.

[Custom validators](custom-validators.md) whose rules use the `namespace::`
axis take more: such a rule makes a node for every namespace in scope of
every element it visits, 170 bytes each. The service counts them in when a
mounted rule uses that axis, and only then.

### How the service sizes itself

The service bounds the memory of the validations in progress, not only their
number:

- **`MAX_CONCURRENT`** (default: the number of CPUs) bounds how many
  validations run at once: the CPU bound. Any request may take one of these
  slots. Beside them there is one express slot, which only a single
  document under 256 KiB may take, as a typical item is: large documents
  that take seconds each run `MAX_CONCURRENT` at a time, and do not keep
  small requests waiting for a slot. A typical item takes about a
  millisecond of CPU, so the one that runs beside `MAX_CONCURRENT` others
  costs them little. With `MAX_CONCURRENT=1` there are two slots as well:
  one for any request and the express slot.

  A package never takes the express slot, however small its ZIP is and
  however it is sent (as `application/zip`, in a multipart upload, or with
  no Content-Type that says so): it waits for one of the `MAX_CONCURRENT`
  slots, as a large document does. A ZIP under 256 KiB can hold hundreds of
  documents that compress well: one of 181 KiB with 600 valid items takes
  1.3 s. On the express slot, one client sending those kept typical items
  waiting 1.2 s (median) while large documents had the other slots; now
  they take 1 ms beside four such clients (6 ms at most).

  The express slot is no CPU of its own. While large documents keep every
  CPU busy, a small request shares the CPUs with them. So that it does not
  wait for one of them to pause, the service runs with one thread more than
  it has CPUs (`GOMAXPROCS` is the number of CPUs plus one), and the kernel
  shares the CPUs between them: with 2 CPUs and 8 clients sending 1 MiB
  documents, a typical item beside them takes 2 ms (median; 7 ms for 9 in
  10) instead of 1 ms, and the large documents are 4% slower than with as
  many threads as CPUs, with which the item took 13 ms (32 ms). Without the
  express slot it would wait for a slot behind them, 240 ms in that test.

  The number of CPUs is what the Go runtime derives from the CPU limit of
  the container (`--cpus`), or else the CPUs of the machine. The default of
  `MAX_CONCURRENT`, and what follows from it, is that number, not the
  number of threads. A `GOMAXPROCS` you set with `-e` is kept as it is, and
  is then the default of `MAX_CONCURRENT` too. The service logs both
  numbers at startup.
- **`MAX_VALIDATION_MEMORY`** bounds what they hold in memory together: the
  memory bound.

Requests beyond `MAX_CONCURRENT` wait for a slot in about the order they
came, at most `MAX_QUEUE` of them (by default 4 × `MAX_CONCURRENT`); beyond
that a request gets 503 at once, with `Retry-After`. A waiting request that
gets no slot within `REQUEST_TIMEOUT` gets 503 too.

A validation reserves its memory as soon as it knows what it needs. To read
a document it reserves 1 MiB plus six times its size, which also covers the
check against the XML Schema: that reads the document as a stream and keeps
little. Once the document is read, it counts its nodes in one quick pass
(some 10 ms per MiB) and reserves what they take in the tree for the rules,
before the schema check, so it waits for memory at most once and with little
work done. In a package every file does so for itself, one after the other.

A validation that finds no room waits for others to finish, as a request
waits for a free slot: at most `MAX_QUEUE` requests wait, and one that gets
no memory within `REQUEST_TIMEOUT`, or that would be one too many waiting,
gets 503 with `Retry-After`. Two large documents that do not fit together
are validated one after the other; neither is slowed down by the other.

A validation that waits holds nothing: no slot, and no memory. It gives up
what it reserved and lets go of the document it read, and reads it again
from the upload once it has its room: from `/tmp`, or for an upload under
256 KiB from the copy the service holds of it (see
[Temporary files](#temporary-files)). So what the validations
reserve together never exceeds `MAX_VALIDATION_MEMORY`, however many
requests wait and whatever `MAX_CONCURRENT` and `MAX_QUEUE` are: with 40
clients sending 10 MiB documents that each take nearly all that one document
may take of a budget of 192 MiB, the process stays 142 MiB above idle. A
validation that has waited two seconds goes before those that came after it,
in the order they came, so documents that fit cannot keep a large one
waiting until it times out.

Typical items are kept apart from all this. A share of the budget is theirs:
4 MiB for every slot, at most an eighth of the budget: 8 MiB with 2 CPUs,
and with 8 CPUs 23 MiB in a 256 MiB container, 32 MiB in a larger one. A
validation that reserves no more than 4 MiB takes
it from that share whenever there is room in it, whatever waits for the
rest; a typical item reserves 2 to 3 MiB. Together with the express slot for
documents under 256 KiB, and a queue of their own of `MAX_QUEUE` places, this
keeps small requests served while large documents queue up: in the test
with 40 clients above, a typical item sent five times a second was validated
every time, in 2 ms. The rest of the budget is what one document may take.
A request under 256 KiB whose document needs more than 4 MiB, one of many
nodes in few bytes, is a large one from then on: it leaves the express slot
for one of the `MAX_CONCURRENT` slots, waiting for one if need be, and takes
its memory from the rest of the budget. So what runs on the express slot
takes its memory from the share of the typical items only, and the memory
bound is the same with the express slot as without it.

The documents of a package, and documents of 256 KiB to about 500 KiB,
reserve no more than 4 MiB either, and take it from that share too, on one
of the `MAX_CONCURRENT` slots. They hold it only while they have their slot:
one that waited for room there and then finds its slot taken gives the room
back, waits for a slot, and takes room again when it has one. So nothing
that waits for a slot behind large documents keeps a typical item waiting
for memory.

Only a document that could never fit, with nothing else running, is refused.
The answer is 413 with code `too_large` and the numbers:

```json
{
  "code": "too_large",
  "message": "the document has 2.6 million nodes and needs about 654 MiB of memory to check; this service allows 176 MiB for one document (MAX_VALIDATION_MEMORY)"
}
```

It is refused as soon as it is read, before the schema check. In a package
this is an error of that file, with the same code and message; the other
files are validated as usual.

`MAX_VALIDATION_MEMORY` is derived at startup from the memory limit: the
limit, less 72 MiB for the process itself, less the upload spool when `/tmp`
is a tmpfs (see [Temporary files](#temporary-files)). In a 256 MiB container
with a volume on `/tmp` and 2 CPUs it is 184 MiB: 8 MiB for small documents
and 176 MiB for one large one, which the service logs at startup. It is
never less than 32 MiB, of which one document may take 28 MiB; in a
container that small, or with `/tmp` in memory, any document over 4.5 MiB
gets 413, as reading it alone takes more.

The memory limit is the least of the limits of the service's control group
and of every group above it (cgroup v2 `memory.max` and `memory.high`, or
cgroup v1 `memory.limit_in_bytes`), so a limit on the pod or on a systemd
slice counts like one on the container, and never more than the memory of
the machine. When there is no limit at all the budget is 1 GiB, or the
memory of the machine less 72 MiB if that is less, and the service logs a
warning: set `MAX_VALIDATION_MEMORY`, or a memory limit, on a host with less
to spare.

### Sizing the container

With 256 MiB the service validates any QTI content up to `MAX_REQUEST_SIZE`
(10 MiB) that is made of text, interactions and declarations, and refuses
documents of more than some 700,000 nodes. To take every document up to
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

The service also sets the soft memory limit of the Go runtime
(`GOMEMLIMIT`) at startup, to the memory limit less 32 MiB, and logs it. Far
below it the garbage collector runs as usual; near it the collector works
harder, which keeps the process within the container. The budget of the
validations and the idle process together stay below it: 184 + 35 MiB, with
a soft limit of 224 MiB in a 256 MiB container. With documents made to fill
that budget the process peaked at 233 MiB. The `HEALTHCHECK` of the image
is a second process in the container, for 10 ms every 30 s: it peaks at
10 MiB, which fits in what is left of the 256 MiB. The image sets no
`GOMEMLIMIT` of its own. (It used to set 90 MiB, which large documents exceed anyway: the
collector then ran without pause, and a 10 MiB document took 2.6 s instead
of 1.7 s, for the same peak.) A `GOMEMLIMIT` you set with `-e` is kept; it
never makes the service fail, only collect sooner. After large documents the
process keeps the memory it used, up to the soft limit, and gives it back to
the system over time.

These figures exclude uploads. Those under 256 KiB are kept in memory, at
most a thirty-second of `MAX_VALIDATION_MEMORY` of them (5.75 MiB in a
256 MiB container, which the service logs at startup as
`bodies_in_memory`), and the others in `/tmp` (see
[Temporary files](#temporary-files)). The bodies in memory come on top of
the budget of the validations: 184 + 35 + 5 MiB for connections + 5.75 MiB
is 230 MiB, above the soft limit of 224 MiB, if every validation took all it
reserved. A validation reserves at least 1.1 times what it takes, so
validations that reserve all 184 MiB take 167 MiB at most, and the process
stays at 213 MiB. If `/tmp` is a tmpfs, its files count
against the container's memory limit too, and the service takes them off the
budget of the validations.

Startup takes under a second, while the schemas of both QTI versions are
compiled; memory briefly peaks at about 100 MB.

## Temporary files

Every request body is received whole before it is validated: up to
`MAX_PACKAGE_SIZE` (100 MiB) for a package, a multipart upload or a body
without an XML Content-Type, and up to `MAX_REQUEST_SIZE` (10 MiB) for a body
sent as `application/xml`.

A body under 256 KiB, which a typical item and a small package are, stays in
memory: no file is made for it. That saves about 0.1 ms per request, which
is a fifth of what a typical item takes, and the page cache its file would
take. Any other body is written to a file in `/tmp`, which is deleted when
the request finishes: one of 256 KiB or more, by its Content-Length or as
soon as it grows that long, and the file of a multipart upload likewise.

The bodies in memory have a budget of their own: a thirty-second of
`MAX_VALIDATION_MEMORY`, at least 4 MiB and at most 32 MiB, which is
5.75 MiB in a 256 MiB container and 30 MiB in one of 1 GiB. It has no
setting. A body reserves the memory it is read into before it takes it,
16 KiB at most to start with and then at most about twice what it has sent
so far, and holds it until its request finishes. When there is no room, the
body goes to a file in `/tmp` instead, as does one that started in memory
and finds no room for its next bytes: such a request is slower by that
0.1 ms, not refused. So the bodies in memory never take more than their
budget, however many connections there are: thousands of connections that
each send most of a small body and then stall hold that budget in memory and
the rest in `/tmp`, where `MAX_SPOOL_SIZE` bounds them.

A body is received in full before the request waits for a validation slot,
so a slow upload holds no slot. An upload must complete within
`REQUEST_TIMEOUT` (60 s), or the request fails with 408; at 60 s, a 100 MiB
package needs about 14 Mbit/s.

`MAX_SPOOL_SIZE` bounds the bytes of all request bodies together, those in
`/tmp` and those in memory; by default it is 4 × `MAX_PACKAGE_SIZE`,
400 MiB. A request reserves its share as its body arrives, so an upload
holds budget only for the bytes it has sent: stalled uploads do not lock
others out, however many there are. An upload whose next bytes do not fit
gets 503 at once, with `Retry-After`. The reservation is freed when the
request finishes. A body that could never fit gets 413 instead: a
Content-Length above the limit of its kind or above `MAX_SPOOL_SIZE` at
once, a chunked body as soon as it grows past either. Give `/tmp` at least
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
work can hold every slot but the express slot for up to `REQUEST_TIMEOUT`,
and validation of packages, and of documents of 256 KiB and more, waits
until then. Documents under 256 KiB go on, on the express slot: a package
cannot take it, whatever its size. What can still hold it is a single
document under 256 KiB made to take long; the work one document may take
grows with its size, so that is bounded more tightly than for a large one.
Rate limits per client (`limit_req`) make all of that harder; a lower
`REQUEST_TIMEOUT`, such as `15s`, shortens it, at the cost of `too_complex`
for the largest legitimate packages.

## Hardening

The image contains only the service binary. It runs as an unprivileged user
and needs no network access. It writes only to `/tmp`, where an upload of
256 KiB or more is kept while it is validated. It can therefore run with a
read-only root filesystem, with a volume for `/tmp`:

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

How the service ends is in the log too:

- A request that fails inside the service is answered with 500
  `internal error` and logged at level `ERROR` with message `panic`, the
  method and path, what failed and the stack. The service goes on.
- What the HTTP server itself reports, such as a connection it could not
  accept, is logged at level `WARN` with `"source":"net/http"`.
- On SIGTERM or SIGINT it logs `shutting down` with the signal, and
  `shutdown complete` when the running requests finished in time. If they
  did not, or if the server stops by itself, it logs `fatal` at level
  `ERROR` with the reason and exits with status 1.

A container that is killed for using more memory than its limit logs
nothing: `docker inspect` shows `OOMKilled`, and Kubernetes the reason
`OOMKilled`.
