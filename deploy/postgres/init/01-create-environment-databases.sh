#!/bin/sh
set -eu

: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${ONEBEAT_PROD_DB_USER:?ONEBEAT_PROD_DB_USER is required}"
: "${ONEBEAT_PROD_DB_PASSWORD:?ONEBEAT_PROD_DB_PASSWORD is required}"
: "${ONEBEAT_TEST_DB_USER:?ONEBEAT_TEST_DB_USER is required}"
: "${ONEBEAT_TEST_DB_PASSWORD:?ONEBEAT_TEST_DB_PASSWORD is required}"

create_environment_database() {
  database_name="$1"
  database_user="$2"
  database_password="$3"

  psql \
    --set ON_ERROR_STOP=1 \
    --set database_name="$database_name" \
    --set database_user="$database_user" \
    --set database_password="$database_password" \
    --username "$POSTGRES_USER" \
    --dbname "$POSTGRES_DB" <<'SQL'
SELECT format(
  'CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %L',
  :'database_user',
  :'database_password'
)
WHERE NOT EXISTS (
  SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = :'database_user'
) \gexec

SELECT format(
  'CREATE DATABASE %I OWNER %I ENCODING %L',
  :'database_name',
  :'database_user',
  'UTF8'
)
WHERE NOT EXISTS (
  SELECT 1 FROM pg_catalog.pg_database WHERE datname = :'database_name'
) \gexec

SELECT format('REVOKE CONNECT ON DATABASE %I FROM PUBLIC', :'database_name') \gexec
SELECT format(
  'GRANT CONNECT, TEMPORARY ON DATABASE %I TO %I',
  :'database_name',
  :'database_user'
) \gexec
SQL
}

create_environment_database \
  "onebeat_prod" \
  "$ONEBEAT_PROD_DB_USER" \
  "$ONEBEAT_PROD_DB_PASSWORD"

create_environment_database \
  "onebeat_test" \
  "$ONEBEAT_TEST_DB_USER" \
  "$ONEBEAT_TEST_DB_PASSWORD"

