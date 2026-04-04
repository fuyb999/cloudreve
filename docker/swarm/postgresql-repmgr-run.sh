#!/bin/bash
# Keep repmgrd talking to the local PostgreSQL over loopback, while
# advertising the stable Swarm service name to peer nodes.

set -o errexit
set -o nounset
set -o pipefail

. /opt/bitnami/scripts/liblog.sh
. /opt/bitnami/scripts/libpostgresql.sh
. /opt/bitnami/scripts/librepmgr.sh
. /opt/bitnami/scripts/postgresql-env.sh

readonly repmgrd_cmd="$(command -v repmgrd)"
readonly local_host="127.0.0.1"
readonly advertised_host="${REPMGR_ADVERTISE_HOST:-$REPMGR_NODE_NAME}"

clean_stale_postgres_state() {
    local -a pg_isready_args=(
        -U postgres
        -p "${REPMGR_PORT_NUMBER}"
        -h "${local_host}"
    )

    if "${POSTGRESQL_BIN_DIR}/pg_isready" "${pg_isready_args[@]}" >/dev/null 2>&1; then
        return 0
    fi

    if [[ -f "${POSTGRESQL_DATA_DIR}/postmaster.pid" || -f "${POSTGRESQL_PID_FILE}" ]]; then
        info "Cleaning stale PostgreSQL pid files before startup..."
        postgresql_clean_from_restart
        rm -f "${POSTGRESQL_PID_FILE}"
    fi
}

psql_super() {
    local -r database="${1:?missing database}"
    local -r query="${2:?missing query}"

    PGPASSWORD="${POSTGRESQL_POSTGRES_PASSWORD}" \
        "${POSTGRESQL_BIN_DIR}/psql" \
        -v ON_ERROR_STOP=1 \
        -h "${local_host}" \
        -U postgres \
        -d "${database}" \
        -tAqc "${query}"
}

wait_for_local_postgres() {
    local -i timeout=60
    local -i step=1
    local -i max_tries=$((timeout / step))
    local -i i

    for ((i = 1; i <= max_tries; i += 1)); do
        if psql_super postgres "SELECT 1" >/dev/null 2>&1; then
            return 0
        fi
        sleep "${step}"
    done

    return 1
}

patch_local_repmgr_conf() {
    repmgr_set_property \
        "conninfo" \
        "user=${REPMGR_USERNAME} $(repmgr_get_conninfo_password) host=${local_host} dbname=${REPMGR_DATABASE} port=${REPMGR_PORT_NUMBER} connect_timeout=${REPMGR_CONNECT_TIMEOUT}" \
        "${REPMGR_CONF_FILE}"
}

ensure_repmgr_prerequisites() {
    local role_exists
    local db_exists

    role_exists="$(psql_super postgres "SELECT COUNT(*) FROM pg_roles WHERE rolname='${REPMGR_USERNAME}'")"
    if [[ "${role_exists}" = "0" ]]; then
        repmgr_create_repmgr_user
    fi

    db_exists="$(psql_super postgres "SELECT COUNT(*) FROM pg_database WHERE datname='${REPMGR_DATABASE}'")"
    if [[ "${db_exists}" = "0" ]]; then
        repmgr_create_repmgr_db
    fi

    psql_super "${REPMGR_DATABASE}" "CREATE EXTENSION IF NOT EXISTS repmgr;"
}

register_primary_if_missing() {
    local node_exists

    node_exists="$(psql_super "${REPMGR_DATABASE}" "SELECT COUNT(*) FROM repmgr.nodes WHERE node_id=${REPMGR_NODE_ID} OR node_name='${REPMGR_NODE_NAME}'")"
    if [[ "${node_exists}" = "0" ]]; then
        info "Registering repmgr primary metadata for ${REPMGR_NODE_NAME}..."
        if [[ "${REPMGR_USE_PASSFILE}" = "true" ]]; then
            PGPASSFILE="${REPMGR_PASSFILE_PATH}" repmgr_execute -f "${REPMGR_CONF_FILE}" master register --force
        else
            PGPASSWORD="${REPMGR_PASSWORD}" repmgr_execute -f "${REPMGR_CONF_FILE}" master register --force
        fi
    fi
}

sync_advertised_conninfo() {
    psql_super "${REPMGR_DATABASE}" "UPDATE repmgr.nodes SET conninfo = regexp_replace(conninfo, 'host=[^ ]+', 'host=${advertised_host}') WHERE node_id=${REPMGR_NODE_ID} OR node_name='${REPMGR_NODE_NAME}';"
}

clean_stale_postgres_state
postgresql_start_bg true
patch_local_repmgr_conf

if ! wait_for_local_postgres; then
    error "PostgreSQL did not become ready on ${local_host}:${REPMGR_PORT_NUMBER}"
    exit 1
fi

if [[ "$(psql_super postgres "SELECT pg_is_in_recovery()")" = "f" ]]; then
    ensure_repmgr_prerequisites
    register_primary_if_missing
    sync_advertised_conninfo
else
    info "Local node is in recovery; skipping repmgr metadata writes on standby."
fi

patch_local_repmgr_conf

info "** Starting repmgrd **"
if am_i_root; then
    exec_as_user "${POSTGRESQL_DAEMON_USER}" "${repmgrd_cmd}" -f "${REPMGR_CONF_FILE}" --daemonize=false
else
    exec "${repmgrd_cmd}" -f "${REPMGR_CONF_FILE}" --daemonize=false
fi
