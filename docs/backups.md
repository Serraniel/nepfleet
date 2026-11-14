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

1. **Read the archive:** it is decrypted with the `age` CLI (needed on the host) and the key sops uses (`--identity` for another one).
   - NepFleet lists the stack's volumes in it: the volumes compose created for the stack's project, found by their labels rather than by name, so stack `app` never touches stack `app_x`.
   - External volumes and bind mounts aren't covered.
   - An unknown `--volume` key is an error. Nothing changes during this step.
2. **Confirm:** type the stack name, or pass `--yes`.
3. **Stop and check:** the stack is stopped if it was running. The restore is refused while containers outside the stack still use one of the volumes.
4. **Keep a safety copy:** each volume's current content is copied to `<volume>-before-restore-<time>`, a new volume labelled `nepfleet.restore.of=<volume>`, so a restore never loses data.
5. **Restore:** each volume is emptied, and the archive's copy is extracted with owners, permissions and links kept.
6. **Start again:** only if the stack was running before.

If a step fails or you press Ctrl-C, the stack stays stopped and NepFleet names the volumes already restored and the safety copies. Once the stack works, remove the copies with the `docker volume rm …` line NepFleet prints.

Restore runs on the host where the stack's volumes are. The app can do it too (a stack's page, **Restore from a backup**): it lists the archives of the host's volume-backup folder, asks for the stack's name, and runs the same steps as a job. Swarm stacks aren't supported yet: scale the service to 0 and restore on the node that holds the volume.
