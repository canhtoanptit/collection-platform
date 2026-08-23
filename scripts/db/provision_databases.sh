#!/usr/bin/env bash
#
# scripts/db/provision_databases.sh -- FND-4 database provisioning.
#
# Creates the databases and per-database owner roles that Terraform deliberately does not own.
# Terraform owns the RDS *instances* (infrastructure/terraform/stacks/20-data); their contents are
# provisioned here, because a database password in a Terraform variable is a password in Terraform
# state and in every plan comment on every pull request.
#
# ==================================================================================================
# WHAT IT CREATES
# ==================================================================================================
#
#   colx-dev-platform  (db.t4g.small)
#     database `ingestion`  owner role `ingestion`   ingestion control plane
#     database `airflow`    owner role `airflow`     Airflow metadata (FND-10)
#     database `keycloak`   owner role `keycloak`    Keycloak realm store (FND-6, KC_DB=postgres)
#     ... plus one database per domain service, appended via PLATFORM_DATABASES as services land
#
#   colx-dev-corebank  (db.t4g.micro, rds.logical_replication=1)
#     database `corebank`   owner role `simulator`   the simulator creates the cb_* tables here
#     role `debezium`                                CDC reader with replication rights
#
# Every generated password is written to AWS Secrets Manager as
# ${SECRET_PREFIX}/db/<role>, never printed and never stored anywhere else.
#
# ==================================================================================================
# IDEMPOTENCY
# ==================================================================================================
#
# Safe to re-run. Every step is a check-then-act:
#
#   - a role that exists is not recreated; its password is converged to the value already in
#     Secrets Manager, so a re-run does NOT rotate credentials out from under running pods
#   - a database that exists is not recreated
#   - grants are re-issued (idempotent in Postgres)
#   - only a role with no secret at all gets a freshly generated password
#
# That property is what makes it safe to run on every rebuild after `make destroy-heavy`.
#
# ==================================================================================================
# ENVIRONMENT CONTRACT
# ==================================================================================================
#
# Required (all available from `terraform output -raw provision_databases_env` in stack 20-data):
#
#   AWS_REGION                      e.g. eu-west-1
#   SECRET_PREFIX                   e.g. colx/dev  -- secrets land at ${SECRET_PREFIX}/db/<role>
#   PLATFORM_HOST                   colx-dev-platform endpoint address (no port)
#   PLATFORM_MASTER_SECRET_ARN      ARN of the RDS-managed master credential secret
#   COREBANK_HOST                   colx-dev-corebank endpoint address (no port)
#   COREBANK_MASTER_SECRET_ARN      ARN of the RDS-managed master credential secret
#
# Optional:
#
#   PLATFORM_PORT                   default 5432
#   COREBANK_PORT                   default 5432
#   PLATFORM_DATABASES              space-separated, default "ingestion airflow keycloak".
#                                   Append service databases here as services land; the owner role
#                                   is always named after the database.
#   SECRETS_KMS_KEY_ARN             CMK for secrets created by this script. Defaults to the
#                                   Secrets Manager AWS-managed key if unset.
#   PGSSLMODE                       default "require" -- RDS supports TLS and there is no reason
#                                   to send a password over plaintext
#   PGCONNECT_TIMEOUT               default 10
#
# ==================================================================================================
# WHERE TO RUN IT
# ==================================================================================================
#
# From inside the VPC. The RDS instances live in the data subnets, which have no inbound route from
# outside, so this runs from a pod in the cluster, an Airflow task, or a bastion -- not a laptop.
#
# Requires: bash 4+, psql (postgresql-client 14+), aws CLI v2, and one of jq or python3 for JSON.
#
#   bash scripts/db/provision_databases.sh --dry-run          # print every command, change nothing
#   bash scripts/db/provision_databases.sh --only platform
#   bash scripts/db/provision_databases.sh
#
# ==================================================================================================

set -euo pipefail

DRY_RUN=false
ONLY="all"

usage() {
	cat <<'EOF'
Usage: provision_databases.sh [--dry-run] [--only platform|corebank|all]

  --dry-run              Print every psql and aws command without executing it. Reads nothing,
                         writes nothing, needs no credentials.
  --only <target>        Provision only one instance. Default: all.
  -h, --help             This help.

See the header of this file for the full environment contract.
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--dry-run) DRY_RUN=true ;;
	--only)
		shift
		[ $# -gt 0 ] || {
			echo "FATAL: --only needs a value (platform|corebank|all)" >&2
			exit 2
		}
		ONLY="$1"
		;;
	--only=*) ONLY="${1#*=}" ;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "FATAL: unknown argument '$1'" >&2
		usage >&2
		exit 2
		;;
	esac
	shift
done

case "$ONLY" in
platform | corebank | all) ;;
*)
	echo "FATAL: --only must be platform, corebank or all (got '$ONLY')" >&2
	exit 2
	;;
esac

# ---------------------------------------------------------------------------------- helpers ----

log() { printf '%s  %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() {
	printf 'FATAL: %s\n' "$*" >&2
	exit 1
}

require_env() {
	local name="$1"
	if [ -z "${!name:-}" ]; then
		die "environment variable $name is required (see the header of this script)"
	fi
}

require_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is not installed or not on PATH"
}

# json_field <json> <key> -- reads one top-level string field. jq preferred, python3 as fallback;
# a hand-rolled sed is deliberately avoided because an RDS-generated password may contain
# characters that break naive quoting.
json_field() {
	local json="$1" key="$2"
	if command -v jq >/dev/null 2>&1; then
		printf '%s' "$json" | jq -r --arg k "$key" '.[$k]'
	elif command -v python3 >/dev/null 2>&1; then
		printf '%s' "$json" | python3 -c 'import json,sys; print(json.load(sys.stdin)[sys.argv[1]])' "$key"
	else
		die "need jq or python3 to parse the Secrets Manager JSON value"
	fi
}

json_object() {
	# json_object k1 v1 k2 v2 ... -- builds a JSON object without shelling out to a template.
	if command -v jq >/dev/null 2>&1; then
		local args=() n
		while [ $# -gt 0 ]; do
			args+=(--arg "$1" "$2")
			shift 2
		done
		# shellcheck disable=SC2016  # the jq program is intentionally single-quoted
		jq -n "${args[@]}" '$ARGS.named'
	elif command -v python3 >/dev/null 2>&1; then
		python3 -c '
import json,sys
a = sys.argv[1:]
print(json.dumps(dict(zip(a[0::2], a[1::2]))))' "$@"
	else
		die "need jq or python3 to build the Secrets Manager JSON value"
	fi
}

# ------------------------------------------------------------------------------ preflight ----

if [ "$DRY_RUN" = true ]; then
	log "DRY RUN -- no command below is executed, no credential is read or written"
else
	require_cmd psql
	require_cmd aws
	require_env AWS_REGION
fi

require_env SECRET_PREFIX

export PGSSLMODE="${PGSSLMODE:-require}"
export PGCONNECT_TIMEOUT="${PGCONNECT_TIMEOUT:-10}"

PLATFORM_PORT="${PLATFORM_PORT:-5432}"
COREBANK_PORT="${COREBANK_PORT:-5432}"
PLATFORM_DATABASES="${PLATFORM_DATABASES:-ingestion airflow keycloak}"

created_roles=0
created_databases=0
written_secrets=0

# ------------------------------------------------------------------------- secrets manager ----

# secret_get_password <secret-name> -- prints the stored password, or nothing if the secret has no
# value yet. A missing secret is an expected state, not an error.
secret_get_password() {
	local name="$1" value
	if [ "$DRY_RUN" = true ]; then
		printf ''
		return 0
	fi
	if ! value="$(aws secretsmanager get-secret-value \
		--region "$AWS_REGION" --secret-id "$name" \
		--query SecretString --output text 2>/dev/null)"; then
		printf ''
		return 0
	fi
	[ -n "$value" ] && [ "$value" != "None" ] || {
		printf ''
		return 0
	}
	json_field "$value" password
}

secret_put() {
	local name="$1" json="$2"

	if [ "$DRY_RUN" = true ]; then
		log "DRY-RUN: aws secretsmanager put-secret-value --secret-id $name --secret-string <redacted>"
		return 0
	fi

	if aws secretsmanager describe-secret --region "$AWS_REGION" --secret-id "$name" >/dev/null 2>&1; then
		aws secretsmanager put-secret-value \
			--region "$AWS_REGION" --secret-id "$name" \
			--secret-string "$json" >/dev/null
	else
		local kms=()
		[ -n "${SECRETS_KMS_KEY_ARN:-}" ] && kms=(--kms-key-id "$SECRETS_KMS_KEY_ARN")
		aws secretsmanager create-secret \
			--region "$AWS_REGION" --name "$name" \
			--description "Database owner credentials, created by scripts/db/provision_databases.sh" \
			"${kms[@]}" \
			--tags Key=project,Value=colx Key=managed-by,Value=provision_databases.sh \
			--secret-string "$json" >/dev/null
	fi
	written_secrets=$((written_secrets + 1))
	log "  secret written: $name"
}

# generate_password -- Secrets Manager's generator first (no local tooling, and it honours the
# exclusion set RDS and Postgres both tolerate), openssl as a fallback.
generate_password() {
	if [ "$DRY_RUN" = true ]; then
		printf 'DRY-RUN-PLACEHOLDER'
		return 0
	fi
	if aws secretsmanager get-random-password \
		--region "$AWS_REGION" \
		--password-length 32 \
		--exclude-punctuation \
		--require-each-included-type \
		--query RandomPassword --output text 2>/dev/null; then
		return 0
	fi
	command -v openssl >/dev/null 2>&1 || die "cannot generate a password: neither Secrets Manager nor openssl is available"
	openssl rand -base64 48 | tr -dc 'A-Za-z0-9' | cut -c1-32
}

# ------------------------------------------------------------------------------------ psql ----

# psql_master <host> <port> <master-user> <master-password> <database> [psql args...]
psql_master() {
	local host="$1" port="$2" user="$3" password="$4" database="$5"
	shift 5

	if [ "$DRY_RUN" = true ]; then
		log "DRY-RUN: psql -h $host -p $port -U $user -d $database $*"
		return 0
	fi

	PGPASSWORD="$password" psql \
		--host="$host" --port="$port" --username="$user" --dbname="$database" \
		--no-password --quiet --tuples-only --no-align \
		--set=ON_ERROR_STOP=1 "$@"
}

# ------------------------------------------------------------------------ role + database ----

# ensure_role <host> <port> <master-user> <master-pw> <role> <secret-name> <database-for-dsn>
#
# Creates the role if absent, then converges its password to whatever is already in Secrets
# Manager (or a freshly generated one if the secret has no value). Re-running never rotates a
# password that is already in use.
ensure_role() {
	local host="$1" port="$2" muser="$3" mpw="$4" role="$5" secret="$6" database="$7"
	local password

	password="$(secret_get_password "$secret")"
	if [ -z "$password" ]; then
		log "  role $role: no stored password, generating one"
		password="$(generate_password)"
	else
		log "  role $role: reusing the password already in $secret"
	fi

	# CREATE ROLE has no IF NOT EXISTS, hence the DO block. NOSUPERUSER/NOCREATEDB/NOCREATEROLE are
	# explicit: an owner role must be able to own its own database and nothing else.
	psql_master "$host" "$port" "$muser" "$mpw" postgres \
		--set=role_name="$role" \
		--command "
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'role_name') THEN
    EXECUTE format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT', :'role_name');
  END IF;
END
\$\$;"

	# Passed as a psql variable and quoted with :'pw', so the password is never interpolated into
	# SQL text by the shell.
	psql_master "$host" "$port" "$muser" "$mpw" postgres \
		--set=role_name="$role" --set=pw="$password" \
		--command "ALTER ROLE :\"role_name\" WITH LOGIN PASSWORD :'pw';"

	created_roles=$((created_roles + 1))

	local json
	json="$(json_object \
		username "$role" \
		password "$password" \
		host "$host" \
		port "$port" \
		database "$database" \
		url "postgresql://${role}@${host}:${port}/${database}?sslmode=require")"
	secret_put "$secret" "$json"

	# Keep the value out of the shell's environment for longer than necessary.
	unset password json
}

# ensure_database <host> <port> <master-user> <master-pw> <database> <owner-role>
ensure_database() {
	local host="$1" port="$2" muser="$3" mpw="$4" database="$5" owner="$6"
	local exists=""

	if [ "$DRY_RUN" = false ]; then
		exists="$(psql_master "$host" "$port" "$muser" "$mpw" postgres \
			--set=db_name="$database" \
			--command "SELECT 1 FROM pg_database WHERE datname = :'db_name';" | tr -d '[:space:]')"
	fi

	if [ "$exists" = "1" ]; then
		log "  database $database: already exists"
	else
		# CREATE DATABASE cannot run inside a transaction block or a DO block, so it is guarded by
		# the query above rather than by IF NOT EXISTS.
		log "  database $database: creating (owner $owner)"
		psql_master "$host" "$port" "$muser" "$mpw" postgres \
			--command "CREATE DATABASE \"$database\" OWNER \"$owner\" ENCODING 'UTF8';"
		created_databases=$((created_databases + 1))
	fi

	# No cross-database access (ADR-0003, A§7.3): revoking CONNECT from PUBLIC means a compromised
	# service role cannot even open a connection to its neighbour's database. Convention and review
	# are not the only thing standing in the way.
	psql_master "$host" "$port" "$muser" "$mpw" postgres \
		--command "
REVOKE ALL ON DATABASE \"$database\" FROM PUBLIC;
GRANT ALL ON DATABASE \"$database\" TO \"$owner\";"

	# The public schema is owned by the master user by default, so the owner role could not create
	# objects in its own database.
	psql_master "$host" "$port" "$muser" "$mpw" "$database" \
		--command "
ALTER SCHEMA public OWNER TO \"$owner\";
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT ALL ON SCHEMA public TO \"$owner\";"
}

# ------------------------------------------------------------------------ platform instance ----

provision_platform() {
	require_env PLATFORM_HOST
	require_env PLATFORM_MASTER_SECRET_ARN

	local master_json="" muser="colxadmin" mpw="DRY-RUN"
	if [ "$DRY_RUN" = false ]; then
		master_json="$(aws secretsmanager get-secret-value \
			--region "$AWS_REGION" --secret-id "$PLATFORM_MASTER_SECRET_ARN" \
			--query SecretString --output text)" ||
			die "cannot read the platform master secret $PLATFORM_MASTER_SECRET_ARN"
		muser="$(json_field "$master_json" username)"
		mpw="$(json_field "$master_json" password)"
	fi

	log "=== colx-dev-platform ($PLATFORM_HOST:$PLATFORM_PORT) as $muser"

	local db
	for db in $PLATFORM_DATABASES; do
		log "--- $db"
		# Owner role is named after the database: database-per-service with a per-database owner
		# (ADR-0003), so `ingestion` owns `ingestion` and can touch nothing else.
		ensure_role "$PLATFORM_HOST" "$PLATFORM_PORT" "$muser" "$mpw" \
			"$db" "${SECRET_PREFIX}/db/${db}" "$db"
		ensure_database "$PLATFORM_HOST" "$PLATFORM_PORT" "$muser" "$mpw" "$db" "$db"
	done

	unset master_json mpw
}

# ------------------------------------------------------------------------ corebank instance ----

provision_corebank() {
	require_env COREBANK_HOST
	require_env COREBANK_MASTER_SECRET_ARN

	local master_json="" muser="colxadmin" mpw="DRY-RUN"
	if [ "$DRY_RUN" = false ]; then
		master_json="$(aws secretsmanager get-secret-value \
			--region "$AWS_REGION" --secret-id "$COREBANK_MASTER_SECRET_ARN" \
			--query SecretString --output text)" ||
			die "cannot read the corebank master secret $COREBANK_MASTER_SECRET_ARN"
		muser="$(json_field "$master_json" username)"
		mpw="$(json_field "$master_json" password)"
	fi

	log "=== colx-dev-corebank ($COREBANK_HOST:$COREBANK_PORT) as $muser"

	# The simulator owns the corebank database: it creates and migrates the cb_* tables (SIM-1), so
	# it has to own the schema.
	log "--- simulator (owner of corebank)"
	ensure_role "$COREBANK_HOST" "$COREBANK_PORT" "$muser" "$mpw" \
		"simulator" "${SECRET_PREFIX}/db/simulator" "corebank"
	ensure_database "$COREBANK_HOST" "$COREBANK_PORT" "$muser" "$mpw" "corebank" "simulator"

	log "--- debezium (CDC reader)"
	ensure_role "$COREBANK_HOST" "$COREBANK_PORT" "$muser" "$mpw" \
		"debezium" "${SECRET_PREFIX}/db/debezium" "corebank"

	# On RDS you cannot grant the REPLICATION attribute directly -- `ALTER ROLE ... REPLICATION`
	# is refused even for the master user, because the master is rds_superuser and not a real
	# superuser. Membership of the `rds_replication` role is RDS's equivalent. On a plain Postgres
	# (local compose, testcontainers) that role does not exist, so fall back to the attribute.
	log "  granting replication rights to debezium"
	psql_master "$COREBANK_HOST" "$COREBANK_PORT" "$muser" "$mpw" postgres \
		--command "
DO \$\$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'rds_replication') THEN
    GRANT rds_replication TO debezium;
  ELSE
    ALTER ROLE debezium WITH REPLICATION;
  END IF;
END
\$\$;"

	# Read access to the tables the simulator will create. ALTER DEFAULT PRIVILEGES is the part
	# that matters: without it, a table created by the simulator *after* this script runs is
	# invisible to Debezium, and the symptom is a connector that starts cleanly and captures
	# nothing.
	log "  granting read access on corebank to debezium"
	psql_master "$COREBANK_HOST" "$COREBANK_PORT" "$muser" "$mpw" postgres \
		--command "GRANT CONNECT ON DATABASE corebank TO debezium;"

	psql_master "$COREBANK_HOST" "$COREBANK_PORT" "$muser" "$mpw" corebank \
		--command "
GRANT USAGE ON SCHEMA public TO debezium;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO debezium;
ALTER DEFAULT PRIVILEGES FOR ROLE simulator IN SCHEMA public GRANT SELECT ON TABLES TO debezium;"

	# Publications are NOT created here. Creating one FOR TABLE requires ownership of the tables,
	# which do not exist until SIM-1 migrates them, and FOR ALL TABLES requires real superuser.
	# ING-5 creates the publication as the `simulator` role after the schema exists.
	log "  note: the Debezium publication is created by ING-5 as the simulator role, after SIM-1 creates the cb_* tables"

	unset master_json mpw
}

# ------------------------------------------------------------------------------------ main ----

case "$ONLY" in
platform) provision_platform ;;
corebank) provision_corebank ;;
all)
	provision_platform
	provision_corebank
	;;
esac

log "=== done: ${created_roles} role(s) ensured, ${created_databases} database(s) created, ${written_secrets} secret(s) written"

if [ "$DRY_RUN" = true ]; then
	log "DRY RUN -- nothing was changed"
fi
