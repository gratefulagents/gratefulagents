# Security policy

## Report a vulnerability privately

Use GitHub's [private vulnerability reporting form](https://github.com/gratefulagents/gratefulagents/security/advisories/new) for this repository. This is the preferred reporting route.

Do **not** open a public GitHub issue for a suspected vulnerability. Do not include credentials, access tokens, private keys, webhook secrets, exploit data from a private deployment, or other sensitive material in public discussions.

A useful private report includes:

- a clear description of the issue and affected component;
- affected version, commit, chart version, or deployment configuration;
- reproducible steps or a minimal proof of concept;
- impact and any known mitigations; and
- contact details only if you want follow-up.

If the private reporting form returns an error or is unavailable, open a public issue that says only that private vulnerability reporting is unavailable. Include no vulnerability details or sensitive material. Maintainers can then establish a private channel before you disclose the report.

## Supported versions

The project is in early development and does not publish a formal security-support window or response-time commitment. Reports are assessed against the current development branch and the latest published release where applicable. Older releases may not receive fixes; do not infer a support guarantee from this policy.

## Disclosure

Please give maintainers time to assess and coordinate a fix before public disclosure. The project does not promise a specific response or remediation timeline.

## Agent-run security scans

The platform's [security scanning feature](user-docs/docs/projects/security-scanning.md) runs AI agents that report findings about scanned repositories. These scans are a research aid, not a security guarantee: the scanned code is untrusted input to the scanning agents, findings are AI-generated and may be wrong or incomplete, and every finding requires human validation before it is treated as a real vulnerability. Do not forward unvalidated scan output as a vulnerability report. If a validated finding affects this project, report it privately through the process above.
