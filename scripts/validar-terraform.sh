#!/usr/bin/env bash
# Confere a sintaxe e os tipos do Terraform de produção, sem credenciais e sem tocar na AWS:
# `init -backend=false` baixa só os providers e `validate` checa os arquivos. Roda no lab (que
# tem Docker e acesso ao registry do Terraform):
#
#   cd ~/client-go-validacao && bash scripts/validar-terraform.sh
#
# Nada aqui cria recurso. O plano real (`terraform plan`) precisa de credenciais da AWS e
# das saídas do Terraform do Booklit; veja docs/producao.md.
set -euo pipefail
RAIZ="$(cd "$(dirname "$0")/.." && pwd)"
TF=(docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$RAIZ/infra:/infra" hashicorp/terraform:1.9)

echo "→ terraform fmt (diferenças aparecem abaixo; rode 'terraform fmt -recursive' para corrigir)"
"${TF[@]}" -chdir=/infra fmt -check -recursive -diff || echo "(formatação diferente do padrão: não é erro, mas vale corrigir)"

for ambiente in prod floci; do
	echo "→ validate infra/envs/$ambiente"
	"${TF[@]}" -chdir=/infra/envs/$ambiente init -backend=false -input=false -no-color >/dev/null
	"${TF[@]}" -chdir=/infra/envs/$ambiente validate -no-color
done
echo "TUDO OK"
