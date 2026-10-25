#!/usr/bin/env bash
# Valida a fase 2 do Client_GO contra o Floci e escreve tudo em relatorio-floci.txt.
#
# Roda na máquina onde o Floci está (o lab). Precisa de Docker; Go e Terraform são opcionais:
# se não estiverem instalados (ou o Go for anterior ao 1.24), roda pelas imagens oficiais.
#
#   bash scripts/validar-floci.sh
#
# Variáveis opcionais:
#   FLOCI          endpoint do Floci (padrão http://localhost:4566)
#   AVISO_DB_URL   usa este Postgres em vez de subir o do docker-compose
#   TF             binário do Terraform (padrão: terraform, ou a imagem hashicorp/terraform)
#   GO             binário do Go (padrão: go 1.24+, ou a imagem golang:1.24)
#
# O relatório não contém o código de OTP de teste: a última verificação confere justamente isso.

set -uo pipefail

RAIZ="$(cd "$(dirname "$0")/.." && pwd)"
cd "$RAIZ"
RELATORIO="$RAIZ/relatorio-floci.txt"
LOG_AVISO="$RAIZ/client-go-validacao.log"
FLOCI="${FLOCI:-http://localhost:4566}"
PORTA_API=18081
CODIGO_TESTE="905317" # valor que não pode aparecer em log nem no banco
CACHE_GO="${XDG_CACHE_HOME:-$HOME/.cache}/aviso-go"
mkdir -p "$CACHE_GO/mod" "$CACHE_GO/build"

: >"$RELATORIO"
FALHAS=0
PID_AVISO=""

registrar() { echo "$*" | tee -a "$RELATORIO"; }
secao() { registrar ""; registrar "=== $* ==="; }
ok() { registrar "OK    $*"; }
falha() { registrar "FALHA $*"; FALHAS=$((FALHAS + 1)); }
# executa, guarda a saída no relatório e devolve o código de saída do comando
rodar() {
	registrar "\$ $*"
	"$@" >>"$RELATORIO" 2>&1
	local codigo=$?
	[ $codigo -ne 0 ] && registrar "(saiu com $codigo)"
	return $codigo
}

encerrar() {
	if [ -n "$PID_AVISO" ] && kill -0 "$PID_AVISO" 2>/dev/null; then
		kill -TERM "$PID_AVISO" 2>/dev/null
		wait "$PID_AVISO" 2>/dev/null
	fi
	secao "Resumo"
	if [ "$FALHAS" -eq 0 ]; then
		registrar "TUDO OK"
	else
		registrar "$FALHAS verificação(ões) falharam"
	fi
	registrar "relatório: $RELATORIO"
}
trap encerrar EXIT

# --- ferramentas --------------------------------------------------------------

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
		-e AVISO_TESTE_DB_URL -e AVISO_TESTE_AWS_ENDPOINT
		-v "$CACHE_GO:/cache" -v "$RAIZ:/src" -w /src golang:1.24)
fi

if [ -n "${TF:-}" ]; then
	TF_CMD=("$TF")
elif command -v terraform >/dev/null; then
	TF_CMD=(terraform)
else
	TF_CMD=(docker run --rm --network host -u "$(id -u):$(id -g)" -e HOME=/tmp
		-v "$RAIZ/infra:/infra" -w /infra/envs/floci hashicorp/terraform:1.9)
fi

secao "Ambiente"
registrar "data: $(date -Iseconds)"
registrar "máquina: $(uname -srm)"
registrar "go: ${GO_CMD[*]:0:1}$( [ "${GO_CMD[0]}" = docker ] && echo ' (imagem golang:1.24)')"
registrar "terraform: ${TF_CMD[*]:0:1}$( [ "${TF_CMD[0]}" = docker ] && echo ' (imagem hashicorp/terraform:1.9)')"
registrar "floci: $FLOCI"
command -v docker >/dev/null && registrar "docker: $(docker --version)"

# --- 1. Floci no ar -------------------------------------------------------------

secao "1. Floci"
if curl -s -o /dev/null --max-time 5 "$FLOCI"; then
	ok "Floci responde em $FLOCI"
else
	falha "Floci não responde em $FLOCI (está no ar? cd ~/floci-lab && docker compose up -d)"
	exit 1
fi

# --- 2. Terraform ---------------------------------------------------------------

secao "2. Terraform (infra/envs/floci)"
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-1
tf() {
	if [ "${TF_CMD[0]}" = docker ]; then
		"${TF_CMD[@]}" "$@"
	else
		(cd infra/envs/floci && "${TF_CMD[@]}" "$@")
	fi
}
if rodar tf init -input=false -no-color && rodar tf apply -auto-approve -input=false -no-color -var "endpoint=$FLOCI"; then
	ok "terraform apply"
else
	falha "terraform apply (veja a saída acima)"
	exit 1
fi
TOPICO="$(tf output -raw topico_arn 2>/dev/null)"
FILAS="$(tf output -raw aviso_filas 2>/dev/null)"
registrar "topico_arn  = $TOPICO"
registrar "aviso_filas = $FILAS"
[ -n "$TOPICO" ] && [ -n "$FILAS" ] || { falha "outputs do Terraform vazios"; exit 1; }

# --- 3. Postgres ----------------------------------------------------------------

secao "3. Postgres"
DB_EXTERNO=""
if [ -n "${AVISO_DB_URL:-}" ]; then
	DB_EXTERNO=1
	registrar "usando AVISO_DB_URL já definida"
else
	# Recria o volume do Client_GO para os scripts de db/ rodarem do zero. Só mexe no volume
	# deste compose (aviso_pgdata), nada do Booklit.
	rodar docker compose down -v
	rodar docker compose up -d --wait || { falha "Postgres do compose não subiu"; exit 1; }
	AVISO_DB_URL="postgres://aviso:aviso@localhost:5433/aviso?sslmode=disable"
fi
ok "Postgres pronto"

# --- 4. go mod tidy e testes -----------------------------------------------------

secao "4. Dependências e testes"
if rodar "${GO_CMD[@]}" mod tidy; then ok "go mod tidy (go.sum gerado com verificação do sum.golang.org)"; else falha "go mod tidy"; exit 1; fi
rodar "${GO_CMD[@]}" vet ./... && ok "go vet" || falha "go vet"

export AVISO_TESTE_DB_URL="$AVISO_DB_URL"
export AVISO_TESTE_AWS_ENDPOINT="$FLOCI"
if rodar "${GO_CMD[@]}" test -count=1 -v ./...; then
	ok "go test com Postgres e Floci"
else
	falha "go test (os testes que falharam estão acima, procure por '--- FAIL')"
fi
PULADOS="$(grep -c -- '--- SKIP' "$RELATORIO" || true)"
[ "$PULADOS" -eq 0 ] && ok "nenhum teste pulado" || falha "$PULADOS teste(s) pulado(s): as variáveis de integração não chegaram"

# --- 5. Ponta a ponta -------------------------------------------------------------

secao "5. Ponta a ponta: API → SNS → SQS → consumidor"
mkdir -p bin
if ! rodar "${GO_CMD[@]}" build -o bin/client-go ./cmd/client-go; then
	falha "build"
	exit 1
fi

TOKEN="$(head -c 32 /dev/urandom | base64 | tr -d '/+=' | head -c 40)"
AVISO_ENDERECO=":$PORTA_API" AVISO_DB_URL="$AVISO_DB_URL" AVISO_TOKENS="booklit=$TOKEN" \
	AVISO_TOPICO_ARN="$TOPICO" AVISO_FILAS="$FILAS" AVISO_WORKERS=2 AWS_ENDPOINT_URL="$FLOCI" \
	./bin/client-go >"$LOG_AVISO" 2>&1 &
PID_AVISO=$!

for _ in $(seq 1 50); do
	curl -s -o /dev/null "localhost:$PORTA_API/saude" && break
	sleep 0.2
done
if curl -s "localhost:$PORTA_API/saude" | grep -q ok; then ok "Client_GO no ar"; else
	falha "Client_GO não subiu"; cat "$LOG_AVISO" >>"$RELATORIO"; exit 1
fi

pedir() { # chave prioridade template
	local expira
	expira="$(date -u -d '+10 min' +%Y-%m-%dT%H:%M:%SZ)"
	curl -s -D - -o /tmp/aviso-resp.json -X POST "localhost:$PORTA_API/notificacoes" \
		-H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: $1" \
		-d '{"template":"'"$3"'","destinatario":"teste@exemplo.com","prioridade":"'"$2"'","variaveis":{"codigo":"'"$CODIGO_TESTE"'"},"expira_em":"'"$expira"'"}'
}
id_de() { sed -n 's/.*"id":"\([^"]*\)".*/\1/p' /tmp/aviso-resp.json; }
estado_de() {
	curl -s "localhost:$PORTA_API/notificacoes/$1" -H "Authorization: Bearer $TOKEN" |
		sed -n 's/.*"estado":"\([^"]*\)".*/\1/p'
}

declare -A IDS
for chave in alta-1 alta-2 alta-3; do
	pedir "$chave" alta otp-login | grep -q '^HTTP/1.1 202' && ok "POST $chave → 202" || falha "POST $chave"
	IDS[$chave]="$(id_de)"
done
pedir normal-1 normal otp-verificar-email | grep -q '^HTTP/1.1 202' && ok "POST normal-1 → 202" || falha "POST normal-1"
IDS[normal-1]="$(id_de)"

if pedir alta-1 alta otp-login | grep -qi '^Idempotent-Replayed: true'; then
	ok "repetição da chave alta-1 reconhecida"
else
	falha "repetição da chave alta-1 não marcada como Idempotent-Replayed"
fi

for _ in $(seq 1 30); do
	pendentes=0
	for chave in "${!IDS[@]}"; do [ "$(estado_de "${IDS[$chave]}")" = enviada ] || pendentes=$((pendentes + 1)); done
	[ $pendentes -eq 0 ] && break
	sleep 0.5
done
for chave in alta-1 alta-2 alta-3 normal-1; do
	estado="$(estado_de "${IDS[$chave]}")"
	[ "$estado" = enviada ] && ok "$chave chegou a 'enviada'" || falha "$chave está em '$estado' (esperava 'enviada')"
done

entregas="$(grep -c '"msg":"entrega simulada"' "$LOG_AVISO" || true)"
[ "$entregas" -eq 4 ] && ok "4 entregas, nenhuma duplicada" || falha "$entregas entregas no log (esperava 4)"

# o roteamento por prioridade (alta só na email-alta) é verificado pelos testes de integração

# --- 6. Desligamento --------------------------------------------------------------

secao "6. Desligamento gracioso"
inicio=$(date +%s%N)
kill -TERM "$PID_AVISO"
wait "$PID_AVISO"
codigo=$?
PID_AVISO=""
ms=$(( ($(date +%s%N) - inicio) / 1000000 ))
[ $codigo -eq 0 ] && ok "saiu com código 0 em ${ms} ms" || falha "saiu com código $codigo em ${ms} ms"
[ "$(grep -c '"msg":"consumidor parado"' "$LOG_AVISO")" -eq 2 ] && ok "os 2 consumidores pararam" || falha "consumidores não registraram parada"
erros="$(grep -c '"level":"ERROR"' "$LOG_AVISO" || true)"
[ "$erros" -eq 0 ] && ok "nenhum ERROR no log" || falha "$erros linha(s) ERROR no log"

# --- 7. Vazamento do código --------------------------------------------------------

secao "7. O código de teste vazou?"
grep -q "$CODIGO_TESTE" "$LOG_AVISO" && falha "o código aparece no log do Client_GO" || ok "código fora do log"
if [ -z "$DB_EXTERNO" ]; then
	no_banco="$(docker compose exec -T db psql -U aviso -d aviso -tAc \
		"select count(*) from t_notificacao n where n::text like '%$CODIGO_TESTE%'" 2>/dev/null)"
	[ "$no_banco" = 0 ] && ok "código fora do banco" || falha "código encontrado no banco ($no_banco linhas)"
fi

secao "Log do Client_GO"
# o grep acima já garantiu que o código não está aqui
cat "$LOG_AVISO" >>"$RELATORIO"
