#!/usr/bin/env bash
# Deixa o Client_GO de pé no lab (Postgres + tópico/filas no Floci + serviço com SES) para o
# Booklit, que roda no desktop, mandar OTP por ele. Roda no lab, na pasta do projeto:
#
#   cd ~/client-go-validacao
#   bash scripts/subir-no-lab.sh            # sobe (ou reinicia) e imprime o que o Booklit precisa
#   bash scripts/subir-no-lab.sh emails     # últimos e-mails recebidos pelo Floci
#   bash scripts/subir-no-lab.sh estado     # notificações e tentativas no Postgres
#   bash scripts/subir-no-lab.sh logs       # acompanha o log do Client_GO
#   bash scripts/subir-no-lab.sh parar      # derruba o Client_GO (o Postgres continua)
#
# No desktop (WSL ou PowerShell), em outro terminal, deixe o túnel aberto:
#   ssh -L 8081:localhost:8081 lab
#
# Variáveis opcionais:
#   FLOCI    endpoint do Floci (padrão http://localhost:4566)
#   PORTA    porta do Client_GO (padrão 8081; só escuta em 127.0.0.1)
#   RESET=1  apaga o banco do Client_GO antes de subir
#   GO / TF  binários, como no validar-floci.sh (padrão: go 1.24+, ou a imagem golang:1.24)

set -euo pipefail

RAIZ="$(cd "$(dirname "$0")/.." && pwd)"
cd "$RAIZ"
FLOCI="${FLOCI:-http://localhost:4566}"
PORTA="${PORTA:-8081}"
ESTADO_DIR="$RAIZ/.lab"
PID_ARQ="$ESTADO_DIR/client-go.pid"
LOG="$ESTADO_DIR/client-go.log"
TOKEN_ARQ="$ESTADO_DIR/token"
REMETENTE_EMAIL="codigo@client-go.test"
REMETENTE="Client_GO <$REMETENTE_EMAIL>"
CACHE_GO="${XDG_CACHE_HOME:-$HOME/.cache}/aviso-go"
mkdir -p "$ESTADO_DIR" "$CACHE_GO/mod" "$CACHE_GO/build"
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-1

rodando() { [ -f "$PID_ARQ" ] && kill -0 "$(cat "$PID_ARQ")" 2>/dev/null; }

parar() {
	if rodando; then
		kill -TERM "$(cat "$PID_ARQ")"
		for _ in $(seq 1 50); do rodando || break; sleep 0.2; done
		echo "Client_GO parado"
	else
		echo "Client_GO não estava rodando"
	fi
	rm -f "$PID_ARQ"
}

sql() { docker compose exec -T db psql -U aviso -d aviso -tAc "$1"; }

case "${1:-subir}" in
parar) parar; exit 0 ;;
logs) exec tail -n 50 -f "$LOG" ;;
emails)
	curl -s --max-time 5 "$FLOCI/_aws/ses" | head -c 4000
	echo
	exit 0
	;;
estado)
	sql "select to_char(criado_em, 'HH24:MI:SS') || ' | ' || template_nome || ' | ' || estado || ' | ' || coalesce(motivo, '-') || ' | ' || destinatario from t_notificacao order by criado_em desc limit 10"
	echo "--- tentativas"
	sql "select n.chave_idempot || ' | #' || t.numero || ' | ' || t.provedor || ' | ' || t.resultado || ' | ' || coalesce(t.motivo, '-') from t_tentativa_envio t join t_notificacao n on n.id = t.notificacao_id order by t.tentado_em desc limit 10"
	exit 0
	;;
subir) ;;
*) echo "uso: $0 [subir|parar|emails|estado|logs]"; exit 2 ;;
esac

# --- ferramentas (mesma escolha do validar-floci.sh) -----------------------------------

go_serve() {
	local v
	v="$("$1" env GOVERSION 2>/dev/null | sed 's/^go//')" || return 1
	[ "$(printf '%s\n1.24\n' "$v" | sort -V | head -1)" = "1.24" ]
}
if [ -n "${GO:-}" ]; then
	GO_CMD=("$GO")
elif command -v go >/dev/null && go_serve go; then
	GO_CMD=(go)
else
	GO_CMD=(docker run --rm --network host -u "$(id -u):$(id -g)"
		-e HOME=/tmp -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod -e CGO_ENABLED=0
		-v "$CACHE_GO:/cache" -v "$RAIZ:/src" -w /src golang:1.24)
fi
if [ -n "${TF:-}" ]; then
	TF_CMD=("$TF")
elif command -v terraform >/dev/null; then
	TF_CMD=(terraform)
else
	TF_CMD=(docker run --rm --network host -u "$(id -u):$(id -g)" -e HOME=/tmp
		-e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_REGION
		-v "$RAIZ/infra:/infra" -w /infra/envs/floci hashicorp/terraform:1.9)
fi
tf() {
	if [ "${TF_CMD[0]}" = docker ]; then
		"${TF_CMD[@]}" "$@"
	else
		(cd infra/envs/floci && "${TF_CMD[@]}" "$@")
	fi
}

# --- 1. Floci ----------------------------------------------------------------------------

echo "→ Floci em $FLOCI"
curl -s -o /dev/null --max-time 5 "$FLOCI" ||
	{ echo "Floci não responde (cd ~/floci-lab && docker compose up -d)"; exit 1; }

# --- 2. Tópico e filas -------------------------------------------------------------------

if [ -n "${INFRA_TOPICO:-}" ] && [ -n "${INFRA_FILAS:-}" ]; then
	TOPICO="$INFRA_TOPICO"
	FILAS="$INFRA_FILAS"
else
	echo "→ Terraform (tópico e filas)"
	tf init -input=false -no-color >/dev/null
	tf apply -auto-approve -input=false -no-color -var "endpoint=$FLOCI" >/dev/null
	TOPICO="$(tf output -raw topico_arn)"
	FILAS="$(tf output -raw aviso_filas)"
fi
[ -n "$TOPICO" ] && [ -n "$FILAS" ] || { echo "outputs do Terraform vazios"; exit 1; }

# --- 3. Postgres -------------------------------------------------------------------------

echo "→ Postgres"
[ "${RESET:-0}" = 1 ] && docker compose down -v
docker compose up -d --wait

# --- 4. Remetente do SES -----------------------------------------------------------------

if command -v aws >/dev/null; then
	aws --endpoint-url "$FLOCI" sesv2 create-email-identity --email-identity "$REMETENTE_EMAIL" >/dev/null 2>&1 || true
else
	echo "(aws CLI ausente: se o Floci recusar o remetente, crie a identidade $REMETENTE_EMAIL à mão)"
fi

# --- 5. Build e subida -------------------------------------------------------------------

parar >/dev/null
echo "→ build"
mkdir -p bin
"${GO_CMD[@]}" build -o bin/client-go ./cmd/client-go

# O token fica salvo: o Booklit não precisa ser reconfigurado a cada subida.
[ -s "$TOKEN_ARQ" ] || { head -c 32 /dev/urandom | base64 | tr -d '/+=' | head -c 40 >"$TOKEN_ARQ"; chmod 600 "$TOKEN_ARQ"; }
TOKEN="$(cat "$TOKEN_ARQ")"

echo "→ subindo o Client_GO em 127.0.0.1:$PORTA"
AVISO_ENDERECO="127.0.0.1:$PORTA" \
	AVISO_DB_URL="postgres://aviso:aviso@localhost:5433/aviso?sslmode=disable" \
	AVISO_TOKENS="booklit=$TOKEN" AVISO_TOPICO_ARN="$TOPICO" AVISO_FILAS="$FILAS" AVISO_WORKERS=2 \
	AWS_ENDPOINT_URL="$FLOCI" AVISO_PROVEDOR=ses AVISO_SES_REMETENTE="$REMETENTE" \
	nohup ./bin/client-go >"$LOG" 2>&1 &
echo $! >"$PID_ARQ"

for _ in $(seq 1 50); do
	curl -s "localhost:$PORTA/saude" | grep -q ok && break
	sleep 0.2
done
curl -s "localhost:$PORTA/saude" | grep -q ok || { echo "não subiu; últimas linhas do log:"; tail -n 20 "$LOG"; exit 1; }

cat <<MSG

Client_GO no ar (pid $(cat "$PID_ARQ")), logs em $LOG

No desktop, deixe o túnel aberto:
  ssh -L $PORTA:localhost:$PORTA lab

E suba o Booklit com:
  BOOKLIT_EMAIL_HABILITADO=true
  BOOKLIT_EMAIL_PROVEDOR=client-go
  BOOKLIT_CLIENT_GO_URL=http://localhost:$PORTA
  BOOKLIT_CLIENT_GO_TOKEN=$TOKEN

Depois, aqui no lab:  bash scripts/subir-no-lab.sh emails   |   estado   |   logs   |   parar
MSG
