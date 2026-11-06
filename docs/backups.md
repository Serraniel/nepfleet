# Backups

`nepfleet enable backups` adds the `volume-backup` system stack on a host, using [docker-volume-backup](https://offen.github.io/docker-volume-backup/):

- **What is backed up:** every Docker volume on the host, except volumes a template marks `backup: false` (caches, models). Those are left out by name, plus anything your `EXCLUDE_REGEXP` matches.
- **Encryption:** archives are encrypted to the fleet's age keys from `fleet.yaml`, so only the fleet key can read them.
- **Where to:** a local folder, and optionally an SSH/SFTP target such as a Hetzner Storage Box, kept for `RETENTION_DAYS`.
- **Consistent databases:** services labelled `docker-volume-backup.stop-during-backup: "true"` are stopped for the copy, which the templates do for their databases.

Run one per host: `nepfleet enable backups backup-nas --target nas`.

## Restore

```sh
nepfleet restore immich --archive /srv/backups/backup-2026-11-06T03-00-00.tar.gz.age
nepfleet restore immich --archive … --volume db        # only the database volume
```

1. **Read the archive:** it is decrypted with the key sops uses (`--identity` for another one), and NepFleet lists the stack's volumes in it: the volumes compose created for the stack's project (by label, not by name, so stack `app` never touches stack `app_x`). Nothing changes yet.
2. **Confirm:** type the stack name, or pass `--yes`.
3. **Stop and keep a safety copy:** the stack is stopped. Each volume's current content is copied to `<volume>-before-restore-<time>`, a new volume labelled `nepfleet.restore.of=<volume>`, so a restore never loses data.
4. **Restore and start:** each volume is emptied, the archive's copy is extracted with owners, permissions and links kept, and the stack is started again.

If a step fails, the stack stays stopped and NepFleet names the volumes already restored and the safety copies. Once the stack works, remove the copies with the `docker volume rm …` line NepFleet prints.

Restore runs on the host where the stack's volumes are. Swarm stacks aren't supported yet: scale the service to 0 and restore on the node that holds the volume.
