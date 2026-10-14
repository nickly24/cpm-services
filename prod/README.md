# CPM production backend

Server: Ubuntu 24.04, 4 vCPU, 8 GB RAM, external MySQL/S3 and local MongoDB with persistent SSD storage.
The frontend is deployed separately. Public backend: `https://back1.cpm-lms.ru/bff/`.

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

The application/infrastructure `cpm.slice` is bounded to 4.5 GiB, and MongoDB has a separate 1.5 GiB budget in `cpm-mongo.slice`. About 1.75 GiB remains for the host.
Application MemoryHigh is 4 GiB. Individual container limits are peaks, not reserved allocations:
Auth 1.5 GiB (Go soft target 1152 MiB); BFF/Cards/Attendance/Uploads 512 MiB; Core/Exams/Homework/Ratings 768 MiB;
Tests/Calculate 1 GiB; PDF worker 1.5 GiB.
Infrastructure limits: RabbitMQ 768 MiB, Redis 320 MiB, Portainer/Prometheus 256 MiB, Nginx 96 MiB.
Peaks cannot all be reached simultaneously; monitor the shared budget before increasing workload.
Go memory targets leave room for buffers and native PDF tools.
The sum of individual application limits is 10 GiB; infrastructure adds 3.15625 GiB (13.15625 GiB total).
The aggregate hard ceilings remain 4.5 GiB plus 1.5 GiB for MongoDB, so individual maxima cannot all be used at once.
The root-owned `/etc/cpm/prod/resources.compose.yaml` host policy keeps Auth's 1.5 GiB limit and 1152 MiB Go target on both deployments and rollbacks. Its template is `prod/resources.compose.yaml`; immutable release bundles are not edited for runtime resource changes.
This configuration does not prove capacity for 450–700 simultaneous test takers.

## Access and recovery

Public: SSH, HTTP port 80 and HTTPS port 443. API routes: `/bff/`; native application ports are internal.
Portainer 9443, RabbitMQ management 15672, Prometheus 9090 bind to localhost.
Use SSH local forwarding for access. Portainer credentials are in the private bootstrap profile.
The HTTPS frontend can use `https://back1.cpm-lms.ru/bff/` once its API address is switched separately.
Cookies are Secure/HttpOnly/SameSite=Strict. Exact allowed origins include `https://cpm-lms.ru`,
`http://localhost:3000`, `http://185.239.51.138` and `https://back1.cpm-lms.ru`.
HTTP on the backend hostname redirects to HTTPS; localhost/IP readiness stays available for deployment probes.
For an HTTP localhost frontend use a local HTTPS proxy or the dev contour: browser SameSite/Secure rules still apply.

Docker restarts exited processes. The systemd watchdog checks liveness only:
three failed minute checks, at most three restarts per 15 minutes, with a startup grace period.
External DB readiness failures do not trigger watchdog restarts.
Workers use 60-second shutdown windows, the PDF worker 150 seconds.
A failed deployment restores the previous images. If the first release fails, ingress and apps stop;
data and diagnostic infrastructure remain. Database migrations are never run by deployment.

## MongoDB persistence and backups

MongoDB 8.0 runs as the single member of replica set `cpm-rs` for consistent oplog backups.
Its data and journal reside on the server SSD at `/srv/cpm/mongo/data` (bind mount to `/data/db`),
with `/srv/cpm/mongo/configdb` separate. Replacing an image/container never removes these paths.
Mongo is reachable through Docker DNS `mongo:27017`; host port 27017 binds only to localhost.
Backend credentials have read/write access only to `default_db`; administration credentials remain separate.
Tests and Calculate acknowledge writes with majority write concern and `j:true`.
The 512 MiB WiredTiger cache fits inside a 1.5 GiB process budget.

`cpm-mongo-backup.timer` runs once daily at 03:00 Europe/Moscow, without jitter.
Full `mongodump --oplog` archives are encrypted using AES-256-GCM before upload to existing external S3.
Keep the two latest successful encrypted backups locally and the two latest in S3 under this installation's own prefix.
Only after a new archive is uploaded and verified does rotation remove older copies; failed backup runs preserve the previous copies.
The scheduled backup gap is one day plus run duration when backups succeed. A missed timer runs after the server returns (Persistent=true).
This is a single host; disk/host loss requires restoring an external backup.
Encryption key: `/etc/cpm/prod/mongo-backup.key`, with an additional private local copy.
Backup credentials/configuration and the replica-set key are under `/etc/cpm/prod/`, excluded from Git.

Inspect backup status: `systemctl status cpm-mongo-backup.timer cpm-mongo-backup.service`.
Last successful backup: `/srv/cpm/backups/mongo/latest.json`.
Restore into an isolated MongoDB instance before replacing real data; decrypt with
`/opt/cpm/tools/bin/python /opt/cpm/infra/scripts/mongo_backup.py --decrypt ENCRYPTED ARCHIVE`,
then use `mongorestore --gzip --archive=ARCHIVE --oplogReplay` on that isolated instance.
Never point a restore test at the live database and never delete the persistent data directory.

## HTTPS and renewal

Timeweb A record `back1.cpm-lms.ru` points to `185.239.51.138`. Nginx terminates TLS 1.2/1.3.
The bootstrap `nginx.conf` serves HTTP ACME challenges. After issuance, install `nginx.tls.conf`
as `/etc/cpm/prod/nginx.conf` and copy it to `/opt/cpm/infra/nginx.conf`. This host policy takes
precedence over bundled Nginx configurations in both deployment and rollback, including old releases.
The host infrastructure Compose file publishes 443 and mounts the certificate directory and ACME webroot read-only.
Before starting Nginx on a fresh host, create `/etc/letsencrypt` and `/var/lib/letsencrypt`.
Certificates/keys remain on the host, never in Git, images or release artifacts.

Initial issuance: `certbot certonly --webroot -w /var/lib/letsencrypt -d back1.cpm-lms.ru`.
Use the official host `certbot.timer` for renewal checks. Install `scripts/certbot-nginx-reload`
as executable `/etc/letsencrypt/renewal-hooks/deploy/cpm-nginx-reload`; it validates Nginx before reload.
Verify using `certbot renew --dry-run --run-deploy-hooks`. Do not remove ACME HTTP access or
mount only the `live/` directory: its symlinks require the sibling `archive/` directory.
The ACME account was registered without email; an operator can add a monitored address later.
