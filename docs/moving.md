# Moving a fleet to a new installation

The fleet repository holds what should run, and the age key opens its secrets. A new NepFleet installation needs those two things, and nothing restarts when it takes over.

1. **Clone the fleet repository** on the new installation, and check that everything is committed and pushed first:

   ```sh
   git clone git@example.org:me/fleet.git ~/fleet
   ```

2. **Hand over the age key,** the private half, over a safe channel. It goes to `~/.config/sops/age/keys.txt`, or wherever `SOPS_AGE_KEY_FILE` points. Whoever holds it can read every secret, so remove it from the old installation when you're done.

3. **Check the machine:**

   ```sh
   nepfleet doctor --fleet ~/fleet
   ```

4. **Take over what runs,** on each host:

   ```sh
   nepfleet adopt --fleet ~/fleet
   ```

   This starts and stops nothing. It records the rendered stacks as applied, and refuses a stack that isn't running, or one that would need new secrets, which means the repository isn't what's running.

5. **Check that nothing would change:**

   ```sh
   nepfleet plan --fleet ~/fleet
   ```

   It should show no changes. If it does, what runs isn't what the repository says, so look at those before applying.

6. **Stop the old installation** (`nepfleet serve`, timers such as `updates apply` or `watch`), so two installations don't act on one stack. Then create new API tokens and sign-ins: those belong to an installation, and don't move with the repository.

**What doesn't move:** the old installation's state directory holds API tokens, users, the event feed, snapshots and rollback copies. Copy `<state>/api` as well if you want to keep its tokens and users. Without the old state, the first apply after adopting has no earlier version on disk to roll back to, until it has run once.

**Not covered:** only compose stacks on the machine running `adopt`. For swarm stacks, a new installation applying the same files converges on what runs.
