# Quality checks and verified builds

The [CI workflow](https://github.com/originchaindb/originchaindb-go/actions/workflows/ci.yml) tests the
SDK before building a package candidate. A successful build is still an unreleased
candidate. Package publication is paused; no CI job here publishes a package,
creates a release tag, or creates a GitHub Release.

## What CI checks

Native unit/type/static checks run on the supported runtime matrix. Every SDK
also runs identical pinned HTTP contracts: 24 core requests, nine resilience
requests and 21 iteration requests. They check URL preservation, bearer handling,
errors, no automatic write replay, batching, early stop, continuation and malformed
page handling. The [contract guide](contract.md) identifies the shared source pin.

The native API extractor compares the public surface with `api/baseline.json`.
Removing or changing an existing signature fails; additions pass. This is a
conservative source-signature guard, not a proof of binary or behavioral
compatibility. Native type-system and reflection limitations still require API
review and the behavioral contracts. CI never regenerates the baseline.

For an intentional API change, review the API diff and migration impact before
using the workflow's extractor command with `scripts/check_api.py --update`.
Do not update the baseline merely to silence a breaking change. A breaking
published API requires the project's major-version and migration process.

## Reproducible transport benchmarks

The `quality` job uses a pinned major/minor SDK runtime on a standard Ubuntu
24.04 x64 GitHub runner and a local HTTP/1.1 fixture. It performs three fresh
process runs, each with 10 warm-up requests, 100 sequential requests, 64 concurrent
requests at concurrency eight, and 10 approximately 1 MiB responses. It records
p50/p95/p99 latency, throughput, connection counts and peak resident memory.
Compilation is outside request timing; memory sampling includes process startup.
These measurements describe SDK/transport overhead, not database throughput or
production network latency. GitHub runner images can still change underneath the
pinned runtime; compare evidence and rerun a noisy failure before rebasing.

Connection reuse, request counts, response decoding and actual concurrent overlap
are hard checks. The median of three runs is compared with the reviewed
`benchmarks/baseline.json` only for the same platform. A p95 increase fails when it
exceeds both 2x baseline and baseline + 10 ms. Peak RSS fails when it exceeds
both 1.5x baseline and baseline + 32 MiB. The thresholds tolerate shared-runner
noise while catching substantial regressions. Throughput and p50/p99 are reported
for diagnosis rather than used as additional unstable thresholds.

PHP uses eight worker processes for the concurrent workload; startup is included
in its concurrency throughput and RSS reports the largest sampled process, not
their aggregate. Ruby uses eight clients on eight threads, matching its synchronous
per-client transport. Other languages use their native concurrency on one client.

To reproduce, follow the `quality` job's preparation and benchmark command in
`.github/workflows/ci.yml`. Python 3.12 and `psutil==7.2.2` drive the fixture.
Use a distinct platform label for local measurements; do not overwrite the Linux
CI baseline with measurements from another OS. Review raw `sdk-benchmark` artifacts,
source SHA and workflow run before accepting a new baseline. Baseline changes
should explain the measured difference and retain that provenance.

## Package installation and signed provenance

CI builds a native package (or a source archive for Go), then installs it into a
fresh application and compiles/loads the public API, including the new iteration
methods. This catches missing package files and dependencies that source-only
tests can miss. `sdk-verified-build` contains those files and `build.json` with
the exact source revision and SHA-256 hashes. These are CI artifacts with a
14-day retention window, not permanent downloadable releases.

On successful pushes to main, the provenance job creates a GitHub OIDC/Sigstore
build attestation and verifies each file with GitHub CLI. It then retains the
bundle as `sdk-build-provenance`. Pull requests do not receive this signing job.
Download `sdk-verified-build` from the exact successful run, inspect `build.json`,
then verify each intended file before installing it:

```sh
gh attestation verify PATH_TO_ARTIFACT --repo originchaindb/originchaindb-go
```

Check the returned source revision and workflow identity against the run you
intended to consume. An attestation establishes artifact origin; it does not
replace review, tests or a vulnerability assessment. See GitHub's
[artifact attestation verification guide](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/verify-artifact-attestations-with-the-github-cli).

Continue using the README's source installation while registry publication is
paused. When publishing resumes, release instructions must identify the exact
registry coordinate/version, supported runtimes, migration notes, successful CI
run and artifact digest. Install that exact version in a fresh application, verify
the registry's provenance/signature where supported, and run a connection smoke
test before updating production. Never substitute an older registry package for
this source candidate just because it has the same package name.

## Real-server compatibility

Mock contracts cannot establish server compatibility or token lifecycle behavior.
Each SDK includes a native `engine_compatibility` worker under tests or examples.
The private integration harness runs these workers on an isolated engine built
from an exact source revision, with throwaway credentials and namespaces. It
checks hash-token provisioning, expiry, rotation on an existing client, revocation,
tenant isolation, read-only permissions, ungranted reads, bounded writes, keyset
reads and revocation after restart. It records engine/SDK revisions and cleanup.

Certification applies only to the engine revision and deployment mode in a
successful evidence record. It does not certify untested engine versions,
control-plane token issuance, multi-node failover, row-level security or every
permission path. Credentials, private engine source and raw auth state are never
uploaded to these public SDK repositories. See [live testing](live-testing.md)
for the existing manually dispatched application integration checks.
