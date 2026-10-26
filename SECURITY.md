# Security policy

This policy covers every repository in the [napwake](https://github.com/napwake) organization.

## Supported versions

Only Napwake's newest [release](https://github.com/napwake/napwake/releases) gets security fixes. To get a fix, upgrade to it.

## Report a vulnerability

Report it privately, in either of these ways:

- [GitHub's private advisory form](https://github.com/napwake/napwake/security/advisories/new) (preferred)
- An email to security@napwake.dev

Don't open a public issue, discussion or pull request.

Include what you can:

- The version you run (`kubectl napwake version`, and the operator's image tag)
- What an attacker could do, and what access they need first
- Steps to reproduce
- A fix, if you have one

A flaw in a dependency goes to that project. Tell us too if it affects Napwake.

## After you report

You'll get a reply within 3 working days. A fix ships in a new release, with a GitHub security advisory. The advisory credits you, unless you'd rather not be named.

## How releases are secured

What Napwake can do in a cluster, what it talks to, and how to verify a release's signatures and provenance are on the [Security](https://docs.napwake.dev/reference/security/) page.
