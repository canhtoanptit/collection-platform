# Keycloak — realm as code

Plan FND-6 (rewritten 2026-08-23: Keycloak replaces Cognito) and **ADR-0017**, which supersedes
ADR-0011's identity decision. ADR-0011's IRSA workload identity and no-public-ingress decisions stand.

| File | What it is |
|---|---|
| `dev.yaml` | Helm values for `codecentric/keycloakx` **7.2.3** → Keycloak **26.6.4** |
| `realm-colx.json` | The `colx` realm: 23 client scopes, 7 groups, 2 M2M clients. **No secrets.** |
| `client-secrets-job.yaml` | kcadm Job — sets the two client secrets, then asserts the realm matches the file |
| `bootstrap.sh` | `pre` = realm ConfigMap, `post` = run the Job. Helmfile hooks call both |

## Chart choice

`codecentric/keycloakx` 7.2.3, `appVersion: 26.6.4`, image `quay.io/keycloak/keycloak`.

Rejected alternatives, both for concrete reasons:

- **Bitnami `keycloak`** — Bitnami moved its container images behind a subscription in 2025, so a
  pinned tag can stop being pullable. An environment whose teardown story is "rebuild from scratch in
  60 minutes" (ADR-0010) cannot depend on that.
- **`codecentric/keycloak`** (the older chart, still in the same repo) — last app version
  `17.0.1-legacy`, i.e. the Wildfly distribution, which Keycloak dropped after 18.

Plain manifests were the fallback if no chart qualified. One did, so the fallback was not needed —
which also means `helm template` covers this release in `scripts/verify/INF-B.sh` like every other.

## Scopes: why they contain colons

The 23 client scopes are named **exactly** the logical colon-form scopes the contracts and services
use — `cases:read`, `payments:admin`, `webhook:write`, … — and each carries
`include.in.token.scope: "true"`, so they appear verbatim in the access token's `scope` claim.

That is the whole point of the switch. Cognito emits `colx-api/cases.read`, so `platform/authn` needed
a normalization step (strip the resource-server prefix, map `.` → `:`). With Keycloak the token already
says `cases:read`, so the pass-through path applies and the Cognito normalization becomes dormant
compatibility code (plan FND-6, SCOPE-FORMAT RULING).

## Groups: a plain `groups` claim

The seven groups (`strategy-author`, `business-approver`, `risk-approver`, `admin`, `collector`,
`ops-admin`, `analyst`) are emitted by a dedicated `groups` client scope holding an
`oidc-group-membership-mapper` with `full.path: "false"` — so the claim is `["collector"]`, not
`["/collector"]`. Service code compares plain names (ADR-0017), and `/collector` would fail every
check silently.

The `groups` scope is in `defaultDefaultClientScopes`, so the Phase 12 SPA client inherits it without
another edit.

## Client secrets are not here, and cannot be

`realm-colx.json` has no `secret` key on either client. Secrets are set after import by
`client-secrets-job.yaml` from `keycloak-client-secrets` (ESO →
`colx/dev/keycloak/client-{platform-services,simulator}`).

Keycloak *does* support `${VAR}` placeholder substitution in realm import files
([keycloak.org/server/importExport](https://www.keycloak.org/server/importExport)). We verified that and
still chose the Job:

1. Placeholders resolve from the **Keycloak server's own environment**, so both client secrets would
   live as env vars on a long-lived StatefulSet pod — readable by anything that can read the pod spec
   or exec in. The Job holds them for about ten seconds.
2. `--import-realm` **skips a realm that already exists**, so placeholders only ever apply on a first
   boot against an empty database. Rotation would need a second mechanism regardless, and one
   mechanism beats two.

ADR-0017 describes the secrets as "set post-start by a `kcadm.sh` Job reading ESO-synced secrets, then
mirrored to Secrets Manager for workload consumption". The direction here is the stronger form of the
same thing: **Secrets Manager is the source of truth**, ESO syncs it into the cluster, and the Job
pushes it into Keycloak. Nothing has to be mirrored back, and there is no window in which the two
disagree. A workload that needs the client secret reads `colx/dev/keycloak/client-*` — the same entry
the Job read.

## Updating the realm

This is the sharp edge. `--import-realm` **skips existing realms**, so editing `realm-colx.json` and
redeploying does *nothing* to a running Keycloak.

The Job makes that visible rather than silent: it counts the colon-form scopes in the file, counts them
in the realm, and fails the deploy if the realm has fewer. When that fires, the dev path is:

```bash
kubectl -n platform exec -it keycloak-0 -- /opt/keycloak/bin/kcadm.sh config credentials \
  --server http://localhost:8080 --realm master --user <admin> --password <pw>
kubectl -n platform exec -it keycloak-0 -- /opt/keycloak/bin/kcadm.sh delete realms/colx
kubectl -n platform rollout restart statefulset/keycloak      # import runs again
helmfile -f deployment/helmfile.yaml -e dev apply             # re-sets client secrets
```

Deleting the realm is acceptable in dev because the realm holds **no human users** — the SPA client and
its users arrive in Phase 12. Once it does hold users, group memberships would be lost, and the update
path becomes targeted `kcadm` calls (or `kc.sh import --override true` during a maintenance window).
That is prod work, tracked with the rest of the prod deltas.

## Cross-WP prerequisites

| Thing | Owner | Note |
|---|---|---|
| `keycloak` database + role on `colx-dev-platform` | INF-A (`scripts/db/provision_databases.sh`) | `KC_DB_URL_DATABASE=keycloak`, `KC_DB_USERNAME=keycloak` |
| RDS endpoint | stack 20-data output | `database.hostname` is a deliberately invalid placeholder; override with `helmfile --state-values-set platformDbHost=<endpoint>` |
| `colx/dev/keycloak/admin` `{username,password}` | out of band | bootstrap admin |
| `colx/dev/keycloak/db` `{password}` | out of band | must match the RDS role's password |
| `colx/dev/keycloak/client-platform-services` `{secret}` | out of band | any high-entropy string |
| `colx/dev/keycloak/client-simulator` `{secret}` | out of band | any high-entropy string |

## Access

No ingress until Phase 12 (ADR-0011). Use `make keycloak` → <http://localhost:8081>.

Issuer for `platform/authn`: `http://keycloak-http.platform.svc.cluster.local/realms/colx`.

Token check once it is up:

```bash
curl -s -u platform-services:<secret> \
  -d grant_type=client_credentials \
  http://localhost:8081/realms/colx/protocol/openid-connect/token | jq -r .access_token \
  | cut -d. -f2 | base64 -d 2>/dev/null | jq '{scope, groups}'
```

## Prod deltas

| Dev | Prod |
|---|---|
| `sslRequired: none`, `KC_HOSTNAME_STRICT=false` | `sslRequired: all`, real hostname, TLS terminated at the ALB |
| 1 replica, `KC_CACHE=local` | ≥2 replicas, `jdbc-ping` or Infinispan over a dedicated DB |
| Bootstrap admin from a static secret | Bootstrap admin deleted after first setup; admin access via federated IdP |
| Realm re-import by deleting the realm | Targeted `kcadm` migrations, reviewed like schema changes |
| `accessTokenLifespan: 900` | Same or shorter, plus DPoP/mTLS-bound tokens for M2M |
| Events kept 7 days in the DB | Shipped to Loki and retained per the audit policy |
