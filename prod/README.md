# CPM production backend

Server: Ubuntu 24.04, 4 vCPU, 8 GB RAM, external MySQL/MongoDB/S3.
The frontend, HTTPS/domain and load testing are separate stages.

## Release

GitHub Actions → Build backend → Run workflow → source branch `prod`.
After a successful build, copy its run ID.
GitHub Actions → Deploy prod → Run workflow → enter that build run ID.
Builds from `dev` and `UAT` are available but cannot deploy to this server.
Rollback prod restores a saved release ID without rebuilding or changing databases/volumes.
Artifacts are retained for 90 days; deployed bundles and images remain on the server.
All image references use registry digests; no automatic image updates.

The deployment account requires CPM_DEPLOY_SSH_KEY and CPM_SSH_KNOWN_HOSTS GitHub environment secrets.
Image download uses the workflow's short-lived read-only GitHub token; no broad PAT is saved on the server.
Production secrets remain in `/etc/cpm/prod`, separate per service, excluded from Git.
Local connection profile: `dev/local/adminvps/profile.json` (private, not published).

## Resources

The shared `cpm.slice` bounds all CPM containers to 6 GiB, reserving about 1.75 GiB for the host.
MemoryHigh is 5.5 GiB. Individual container limits are peaks, not reserved allocations:
Auth/BFF/Cards/Attendance/Uploads 512 MiB; Core/Exams/Homework/Ratings 768 MiB;
Tests/Calculate 1 GiB; PDF worker 1.5 GiB.
Infrastructure limits: RabbitMQ 768 MiB, Redis 320 MiB, Portainer/Prometheus 256 MiB, Nginx 96 MiB.
Peaks cannot all be reached simultaneously; monitor the shared budget before increasing workload.
Go memory targets leave room for buffers and native PDF tools.
This configuration does not prove capacity for 450–700 simultaneous test takers.

## Access and recovery

Public: SSH and HTTP port 80. API routes: `/bff/`; native application ports are internal.
Portainer 9443, RabbitMQ management 15672, Prometheus 9090 bind to localhost.
Use SSH local forwarding for access. Portainer credentials are in the private bootstrap profile.
HTTP backend cannot serve the existing HTTPS frontend yet; diagnostic login uses an SSH tunnel.
Cookies remain HttpOnly/SameSite=Strict; Secure is explicitly configurable for the HTTPS stage.

Docker restarts exited processes. The systemd watchdog checks liveness only:
three failed minute checks, at most three restarts per 15 minutes, with a startup grace period.
External DB readiness failures do not trigger watchdog restarts.
Workers use 60-second shutdown windows, the PDF worker 150 seconds.
A failed deployment restores the previous images. If the first release fails, ingress and apps stop;
data and diagnostic infrastructure remain. Database migrations are never run by deployment.
