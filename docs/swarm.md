# Swarm: storage, ingress and DNS

## Shared storage

There is no single tool that covers every case, so NepFleet supports three volume classes. A template marks data that must follow a service across nodes with `spec.volumes[].shared: true`.

| Class | Use for | Implementation |
|---|---|---|
| **external** | Media, documents, backups | CIFS, NFS or SFTP shares (for example a Hetzner Storage Box) through Docker's built-in `local` driver with `type=cifs` or `nfs`. No plugin is needed. |
| **replicated** | Files that must survive a node being offline | [SeaweedFS](https://github.com/seaweedfs/seaweedfs), with a filer on each storage node and a replication factor of 2. Nodes may have different disks or no storage at all. |
| **pinned** | Databases | Kept on local disk and pinned to a node with a label, with continuous restic backups. Networked file systems corrupt databases. For real HA, use the database's own replication. |

Not chosen: GlusterFS (development has stalled), Ceph (too heavy for 2–5 nodes), Longhorn (Kubernetes only).

> Research issue: validate SeaweedFS failover behaviour with 2 and 3 nodes before making it the default.

## Ingress without a bottleneck

The problem is that pointing every DNS name at the manager funnels all traffic through a small node.

NepFleet's approach:

1. Nodes labelled `nepfleet.ingress=true` run Caddy in **global mode** with **host-mode ports**. This bypasses the routing mesh and preserves client IPs. Managers do not get this label by default.
2. Caddy reaches services over an overlay network. Certificates are stored in the replicated volume class, so every ingress node can serve every name.
3. **DNS**: NepFleet keeps one A/AAAA record per healthy ingress node through the DNS provider's API. It uses `libdns` providers, the same ones Caddy uses, and removes a node's record when its health check fails or it is drained for an update.
4. Alternative for a single entry point: a floating IP (for example a Hetzner floating IP) moved by NepFleet on failure.

## Rolling operations

- Service updates use `update_config: {parallelism: 1, order: start-first, failure_action: rollback}`. NepFleet adds this by default when a template does not set it.
- Node maintenance follows these steps: drain → remove the node's DNS record → do the work → wait until the node is ready → reactivate → wait for its services to become healthy → move to the next node. Managers go one at a time and only while quorum holds.
