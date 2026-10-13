# Security policy

A browser is a security product. We take reports seriously and fix them before talking about them.

## Supported versions

Sweef is in **alpha**. Only the latest release and `main` receive security fixes. There are no long-term support branches yet.

| Version | Supported |
|---|---|
| latest `v0.x` alpha release | yes |
| `main` | yes |
| older alphas | no, please update |

## Reporting a vulnerability

**Do not open a public issue or pull request for a security problem.**

Use GitHub's private reporting: on the repository open **Security** → **Report a vulnerability**
(https://github.com/sweefbrowser/sweef/security/advisories/new). Include:

* what is affected (component, version or commit, OS),
* steps or a minimal page that reproduces it,
* the impact you believe it has (sandbox escape, same-origin bypass, code execution, data leak, denial of service, ...),
* whether you plan to disclose, and by when.

## What to expect

| Step | Target |
|---|---|
| Acknowledgement | within 3 working days |
| Triage and severity | within 7 days |
| Fix or mitigation for high/critical issues | within 30 days where possible |
| Coordinated disclosure | we agree a date with you; default 90 days after the report, sooner once a fix ships |

We credit reporters in the advisory and the changelog unless you ask us not to.

## Scope

In scope: the browser engine and its networking, cookie/storage/origin isolation, the JavaScript host bindings, the
media/image/font/PDF decoders (they parse hostile input), the agent and MCP interfaces (`sweef agent`, local RPC), the
Sidekick agent's safety and confirmation gates, the auto-update verification library, and the release/packaging scripts.

Out of scope: social engineering, issues that need a pre-compromised machine, missing hardening on unsigned alpha
installers that is already documented in `docs/install.md`, and findings in third-party dependencies that are not reachable
from Sweef (report those upstream).

## Keys and secrets

Sweef never ships a project-owned model API key. Users bring their own key; it is stored in a 0600 file in the profile
(DPAPI-protected on Windows) and is not logged. If you find a path where it is logged, sent elsewhere, or readable by a
web page, that is a vulnerability: please report it.

## Safe harbour

Good-faith research that follows this policy, avoids privacy violations and service disruption, and gives us reasonable
time to fix, will not be pursued legally by the maintainers.
