# money-checks in Kubernetes

`serve` as one Deployment: it reads the databases every 10 minutes, Prometheus
scrapes `/metrics`, and changes go to Slack. Plain manifests, no Helm:

```
kubectl apply -k examples/kubernetes
```

| File | |
|---|---|
| `checks.yaml` | the checks, as a ConfigMap; a change rolls the Deployment |
| `secret.yaml` | placeholders for the database URIs and the Slack webhook |
| `pvc.yaml` | the volume for `serve.state_file` |
| `deployment.yaml` | one replica, `Recreate`, non-root, read-only root filesystem |
| `service.yaml`, `servicemonitor.yaml` | scraping, for the Prometheus Operator |
| `prometheusrule.yaml` | the alerts of the main README |

Before you apply it:

- **Database users.** Give money-checks a user that can only read, best on a
  secondary or a replica: the `read` role in MongoDB, `SELECT` on the tables in
  PostgreSQL. For MongoDB and ClickHouse `serve` checks this at start and refuses
  a user that can write, unless the source sets `require_read_only: false`;
  PostgreSQL runs every query in a read-only transaction.
- **The Secret.** `secret.yaml` holds placeholders. Create the real one from your
  secret store and remove the file from `kustomization.yaml`.
- **The network.** The pod must reach the databases. If a NetworkPolicy denies
  egress by default, allow the database ports, and Slack on 443 when you post there.
- **The image.** Pin an exact version (`images` in `kustomization.yaml`). If the
  cluster pulls only from your own registry, copy the image there with its tag.

Why it looks like this:

- **One replica.** Two would post every message twice, and both would write the
  state file. `Recreate` stops the old pod before the new one mounts the volume.
- **The state file on a volume.** Without it every deploy and every node drain
  posts all the violations again, and people mute the channel.
- **Readiness is the first run.** `/readyz` answers once the first run has
  finished. That is a full pass of every check and can take minutes on a big
  database; until then the pod is not scraped, so no alert fires on empty numbers.
- **A database that goes away** is one line in Slack, `money_check_source_up` 0,
  and a retry after a minute, not a failure per check
  ([ADR 0007](../../docs/adr/0007-source-down-one-message.md)).

`kubectl kustomize examples/kubernetes` renders the manifests; CI validates them
with kubeconform, and a test parses `checks.yaml`.
