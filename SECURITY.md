# Security

## Reporting a vulnerability

Report vulnerabilities privately through GitHub: on
[github.com/Jenish-1235/dsbox](https://github.com/Jenish-1235/dsbox), open the
**Security** tab and choose **Report a vulnerability**. Don't open a public
issue for a security problem.

Include what is affected (infrastructure code, provisioning, `ds`, a
component), how to reproduce it, and what an attacker could do with it. You
will get a reply in the advisory thread.

## Threat model

dsbox assumes:

- **The tailnet is the perimeter.** A devbox accepts no inbound traffic from
  the internet except Tailscale's WireGuard port. Anyone on the tailnet who is
  allowed by its access controls can reach SSH, project links and database
  ports, and dev servers listen on all interfaces. Tailscale's ACLs decide who
  that is.
- **The developer is trusted on their own devbox.** They have sudo, and
  agents such as Claude Code run as them with the same rights. dsbox does not
  sandbox agents from the machine.
- **Containers are not trusted with AWS credentials.** IMDSv2 with a hop limit
  of 1 keeps the instance role's credentials out of Docker containers. With
  `instance_admin` on, anything running on the host as the developer can
  manage the AWS account.
- **The operator's AWS credentials are the break-glass path.** SSM gives a
  root-capable shell to anyone who can start a session on the instance.
- **Secrets stay out of the repository.** Per-devbox values and OpenTofu state
  live in a private ops folder; the machine settings file is public. Logins
  (`gh`, `glab`, Claude Code) are the developer's, stored on the devbox.

Out of scope: the security of Tailscale, AWS, ntfy and the developer's own
projects and dependencies. Known trade-offs are listed under
[Security model](README.md#security-model) in the README, including the token
a GitLab push mirror to GitHub stores on GitLab and the ntfy topic that anyone
who knows it can read.
