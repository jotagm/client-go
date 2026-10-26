#!/bin/bash
# Garante o usuário e o database "aviso" no RDS do Booklit. O deploy.sh chama este script a
# cada deploy: é idempotente (não recria nada, só alinha a senha com a do SSM) e roda NA HOST,
# num container descartável; o container do Client_GO nunca vê a senha do usuário mestre.
#
# O RDS não tem IP público, então só a EC2 alcança.
set -euo pipefail

REGIAO="${AWS_REGION:-us-east-1}"
param() { aws ssm get-parameter --region "$REGIAO" --name "$1" --with-decryption --query Parameter.Value --output text; }

# AVISO_DB_URL = postgres://aviso:<senha>@<endereço do RDS>:5432/aviso?sslmode=require
URL_AVISO="$(param /client-go/prod/AVISO_DB_URL)"
RESTO="${URL_AVISO#postgres://aviso:}"
export SENHA_AVISO="${RESTO%%@*}"
ENDERECO="${RESTO#*@}"
ENDERECO="${ENDERECO%%:*}"
[ -n "$SENHA_AVISO" ] && [ -n "$ENDERECO" ] || { echo "AVISO_DB_URL fora do formato esperado" >&2; exit 1; }

# Mesmo banco do Booklit: o usuário mestre dele ("booklit") cria o papel e o database.
export PGPASSWORD
PGPASSWORD="$(param /booklit/prod/DATABASE_PASSWORD)"

# As duas senhas viajam por variável de ambiente (-e sem valor herda do shell), nunca pela linha
# de comando, que apareceria em `ps`. O psql as lê com \getenv.
docker run --rm -i -e PGPASSWORD -e SENHA_AVISO postgres:16-alpine \
  psql "host=$ENDERECO dbname=booklit user=booklit sslmode=require" -v ON_ERROR_STOP=1 <<'SQL'
\getenv senha SENHA_AVISO

-- Cria o usuário se não existir; senão só alinha a senha com a do SSM.
SELECT format('CREATE ROLE aviso LOGIN PASSWORD %L', :'senha')
  WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aviso') \gexec
SELECT format('ALTER ROLE aviso PASSWORD %L', :'senha')
  WHERE EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aviso') \gexec

-- No RDS o usuário mestre precisa ser membro do papel para criar um database que seja dele.
GRANT aviso TO booklit;
SELECT 'CREATE DATABASE aviso OWNER aviso'
  WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'aviso') \gexec

-- O aviso só enxerga o próprio database.
REVOKE ALL ON DATABASE aviso FROM PUBLIC;
GRANT CONNECT ON DATABASE aviso TO aviso;
SQL

echo "Banco 'aviso' pronto em $ENDERECO."
