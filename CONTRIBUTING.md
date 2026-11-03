# Contributing

Run the checks in the README before submitting a pull request. Keep public APIs
idiomatic and the shared behavior contract consistent across all eight SDKs.
Add a regression case for observable behavior changes, including failure paths.

Use environment variables for credentials. Tests must use a local fixture or an
explicitly selected integration database; never include credentials or customer
data in fixtures, logs, issue reports, or commits.

Changes to authentication, retry behavior, result shapes or parameter binding
must update the shared contract and its tests. Do not generate public API wrappers
from OpenAPI. Review the language's transport, cancellation and error conventions.

CI builds and tests source and package artifacts. It does not publish packages or
create releases. Publication requires a separate approved release task.
