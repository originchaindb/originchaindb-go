# Database access tokens

Use the complete database HTTPS address from **Connect** as `OC_URL` and a
database access token as `OC_TOKEN`. A SQL-tool username/password is a separate
credential. The SDK sends the access token in the Authorization header.

## Set up access

1. Select the database when creating your access token. Give it `read-data` for
   reads and `write-data` only when the application needs to write.
2. Ensure your account has a linked database identity on that database. Its
   roles, table grants, row policies, and column masks still apply to the token.
   An administrator can set up the identity and the permissions you need.
3. Wait for the token's database access state to become **active**. Creation of
   the control-plane token and activation on the database are separate steps.
4. Connect and run a read against a table the identity may access. `SELECT 1`
   alone does not verify a table grant, row policy, or column mask.

The token's selected capabilities and the database identity both limit access.
Row policies and column masks are enforced by the server; the SDK does not
grant additional permissions.
Feature availability and supported instance configurations are server concerns;
check the database access state shown for your selected instance.

## Rotate an application credential

Use the SDK's application-managed token provider when the credential can change
while the process is running. See the native example in the
[API guide](api/v1/README.md). The provider is called for each outgoing request;
your application supplies the current secret from its own configuration or
secret store. The SDK does not sign in to the console or manage that store.

After regeneration, wait for the replacement token to become active before
using it. Regeneration can leave a propagation interval; these SDKs do not
promise uninterrupted credential rotation. Do not use a write as a readiness
probe or automatically replay a failed write while waiting.

Deleting a token in the control plane also requires propagation to its database
targets. Use the control plane's database revocation receipt to confirm removal.
Do not assume an immediate HTTP 401 after pressing Delete, or treat a pending
receipt as confirmed revocation.

## Diagnose access failures

| Result | Check |
| --- | --- |
| HTTP 401 | The token is valid, unexpired, and active on the selected database; refresh the application's credential after rotation. |
| HTTP 403 | The token covers this database and operation, and the linked identity has the required permissions. |
| Fewer rows or masked columns | Check the identity's row policies and column masks; successful HTTP status does not mean unrestricted data. |
| Timeout or interrupted write | Reconcile the operation's outcome before retrying; the server may have committed it. |

Keep secrets out of source control and request logs. Use the SDK's request
observer for status and timing without recording credentials or query contents.

## Verification

All eight SDKs passed the same 64-check live qualification on 4 November 2026:
control-plane issuance, exact row filtering and masking, ungranted-table and
read-only write refusal, invalid-token rejection, rotation on existing clients,
and deletion followed by confirmed database revocation. Temporary test resources
were removed. This verifies the tested paths and deployment, not every possible
security policy or server release. See the current
[SDK support matrix](https://github.com/originchaindb/.github/blob/main/SDK-SUPPORT.md)
for scope and limitations.
