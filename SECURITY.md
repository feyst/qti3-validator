# Security

## Supported versions

Only the latest release gets security fixes. The image is rebuilt and
released by itself when a dependency or the Go version has a fix, so
`feyst/qti3-validator:latest`, or the newest `X.Y` tag, is always the one to
run.

## Reporting a vulnerability

Please do not open a public issue. Report it privately through
[GitHub's vulnerability reporting](https://github.com/feyst/qti3-validator/security/advisories/new),
with the request or package that triggers it and what happens. You will get an
answer within a week; a fix is released as soon as it is ready, and you are
credited in the advisory unless you prefer not to be.

The service parses documents and ZIP packages from untrusted clients, so
reports about resource use (memory, CPU, ZIP bombs), XML parsing and path
handling in packages are especially welcome. See
[Running in production](docs/deployment.md) for the limits that are meant to
contain them.
