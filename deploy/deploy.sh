#!/bin/bash
# Deploy do Client_GO na EC2. Chamado pelo pipeline via SSM Run Command (que antes grava este
# arquivo e o compose.yaml em /opt/client-go):
#   /opt/client-go/deploy.sh <conta>.dkr.ecr.<regiao>.amazonaws.com/client-go:<sha>
# Também serve à mão pelo Session Manager.
#
# 1. login no ECR  2. configuração do SSM -> app.env  3. banco e migrações  4. sobe a imagem nova
# 5. espera o healthcheck do container; se não ficar saudável em 2 min, volta para a anterior.
#
# As migrações só adicionam (colunas e tabelas), então a imagem anterior continua funcionando
# em cima do esquema novo: voltar a imagem basta, não há "desfazer" de banco.
set -euo pipefail

IMAGEM="${1:?uso: deploy.sh <imagem:tag>}"
REGIAO="${AWS_REGION:-us-east-1}"
PREFIXO="${PREFIXO_SSM:-/client-go/prod}"
cd /opt/client-go

# O Client_GO entra na rede que o compose do Booklit cria. Sem o Booklit de pé, não há rede.
if ! docker network inspect "${REDE_BOOKLIT:-booklit_default}" >/dev/null 2>&1; then
  echo "Rede ${REDE_BOOKLIT:-booklit_default} não existe: faça o deploy do Booklit antes" >&2
  exit 1
fi

REGISTRO="${IMAGEM%%/*}"
aws ecr get-login-password --region "$REGIAO" | docker login --username AWS --password-stdin "$REGISTRO"

# Segredos só no disco desta máquina, legíveis só pelo root.
umask 077
aws ssm get-parameters-by-path --region "$REGIAO" --path "$PREFIXO" --recursive --with-decryption \
  --query 'Parameters[].[Name,Value]' --output text \
  | awk -F'\t' -v prefixo="$PREFIXO/" '{ sub("^" prefixo, "", $1); print $1 "=" $2 }' > app.env.novo
for obrigatoria in AVISO_DB_URL AVISO_TOKENS AVISO_TOPICO_ARN AVISO_FILAS AVISO_SES_REMETENTE; do
  if ! grep -q "^${obrigatoria}=." app.env.novo; then
    echo "Configuração incompleta em $PREFIXO (falta $obrigatoria); abortando" >&2
    rm -f app.env.novo
    exit 1
  fi
done
mv app.env.novo app.env

ANTERIOR="$(grep '^IMAGE=' .env 2>/dev/null | cut -d= -f2- || true)"
escrever_env() { printf 'IMAGE=%s\nAWS_REGION=%s\n' "$1" "$REGIAO" > .env; }

escrever_env "$IMAGEM"
docker compose pull client-go

# Garante o usuário e o database "aviso" no RDS (idempotente; roda na host, não no container).
echo "Garantindo o banco"
bash ./criar-banco-prod.sh

echo "Aplicando migrações"
docker compose run --rm --no-deps client-go migrar

docker compose up -d --remove-orphans

# O container tem HEALTHCHECK (subcomando "sonda"); aqui só esperamos ele ficar "healthy".
for _ in $(seq 1 24); do
  ESTADO="$(docker inspect -f '{{.State.Health.Status}}' client-go 2>/dev/null || echo ausente)"
  if [ "$ESTADO" = healthy ]; then
    echo "Deploy ok: $IMAGEM"
    docker image prune -af --filter "until=168h" >/dev/null || true
    exit 0
  fi
  sleep 5
done

echo "O Client_GO não ficou saudável em 2 minutos (estado: $ESTADO)." >&2
docker compose logs --tail 80 client-go >&2 || true
if [ -n "$ANTERIOR" ] && [ "$ANTERIOR" != "$IMAGEM" ]; then
  echo "Voltando para $ANTERIOR" >&2
  escrever_env "$ANTERIOR"
  docker compose up -d
fi
exit 1
