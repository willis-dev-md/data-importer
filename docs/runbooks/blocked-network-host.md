# Runbook: A download fails with "CONNECT tunnel failed, response 403"

**Owner role:** developer / environment owner  |  **Severity if this happens:** medium
**Last verified:** 2026-11-20, 027d2e2 (symptom and diagnosis reproduced; the settings change needs the environment owner)

## When to use
In a Claude Code cloud session (outbound HTTPS goes through an allow-listed proxy) a fetch of a publisher's file fails,
for example when onboarding an ONS, Bank of England, GOV.UK or Land Registry source.

## Prerequisites
Edit rights on the cloud environment (Settings of the environment the session runs in). Package registries are allowed by default.

## Steps
1. Reproduce and read the status: `curl -sS -L -m 40 -o /dev/null -w "%{http_code}\n" "<url>"`
   *(expected when blocked: `curl: (56) CONNECT tunnel failed, response 403` and `000`)*.
2. Note the **host** of the URL (for example `www.ons.gov.uk`, `www.bankofengland.co.uk`, `www.gov.uk`, `publicdata.landregistry.gov.uk`).
3. Add exactly that host to the environment's **Network access → Allowed domains** (cloud environment menu in the session title bar → Edit). Wildcards are not accepted; add each host.
4. Wait about 30 seconds, then repeat step 1 *(expected: `200`)*.

## Troubleshooting
| Symptom | Diagnosis | Fix |
|---|---|---|
| Still 403 after adding the host | change not yet applied | wait 30 s and retry once |
| 200 for the landing page, 403 for the file | files live on another host (e.g. GOV.UK pages link to `publicdata.landregistry.gov.uk`) | read the file URL from the page and allow its host too |
| 403 only from the importer, curl works | importer uses a different proxy setting | run with the proxy variables of the session; see `/root/.ccr/README.md` in cloud sessions |

## Verify
`curl -sS -L -m 60 -o /dev/null -w "%{http_code}\n" "<url>"` prints `200`.

## Roll back / undo
Remove the host from Allowed domains in the same environment settings.

## Escalate
Environment owner; include the host and the exact curl error.
