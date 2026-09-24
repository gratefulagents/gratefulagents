# Bundled object storage

The chart uses [PGSTY Silo](https://github.com/pgsty/silo), a maintained MinIO
fork, for S3-compatible activity logs and artifacts. Both the server and the
bucket-creation hook use the pinned standard Silo image; the hook runs its
bundled `mcli` client. It no longer pulls upstream MinIO server or client images.

## Upgrading from MinIO

- Back up object data and test recovery before upgrading. Review Silo's
  [migration guide](https://silo.pgsty.com/compatibility/migration/) and release
  notes; storage-format compatibility is not a substitute for a tested backup.
- Keep the existing Helm release name, namespace, `minio` credentials, bucket,
  and persistence settings. The `minio` values key, resource names, selectors,
  service endpoint, and PVC name intentionally remain unchanged, so the new
  server mounts the existing data. `MINIO_*` environment variables remain valid.
- If your values file explicitly sets `minio.image` or `minio.mcImage`, remove
  those overrides to use the new defaults, or set both to the pinned Silo image
  in `values.yaml`. The hook image must provide `/bin/sh` and `mcli`; upstream
  `mc` images and Silo's distroless image are not suitable for this hook.
- The single-replica server uses a `Recreate` deployment strategy, so expect
  storage downtime during the upgrade. The post-upgrade hook creates the bucket
  idempotently with `mcli mb --ignore-existing`.

External S3 configuration is unchanged: set `minio.enabled=false` and configure
`objectStorage` to use your existing provider.

The standalone `config/test/minio.yaml` fixture uses the same Silo image and
client. When reapplying it to an existing test cluster, delete the old
`sample-minio-create-bucket` Job in namespace `sample-minio` first, since
Kubernetes Job pod templates are immutable. The Helm hook handles its own Job
replacement.
