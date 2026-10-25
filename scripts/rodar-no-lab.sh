#!/usr/bin/env bash
# Roda no WSL: copia o projeto para o lab, executa a validação no Floci de lá e traz o
# relatório de volta para a pasta do projeto (onde o Claude consegue ler).
#
#   bash /mnt/d/Projetos/aviso/scripts/rodar-no-lab.sh
#
# Variáveis opcionais:
#   LAB      alias ou host do SSH (padrão: lab)
#   DESTINO  pasta no lab (padrão: ~/client-go-validacao, recriada a cada execução)

set -euo pipefail

LAB="${LAB:-lab}"
DESTINO="${DESTINO:-client-go-validacao}"
# o alias do lab pode ter LocalForward (4566, 9092...); aqui não precisa, e evita o
# "Address already in use" quando outra sessão SSH já ocupa as portas
SSH_OPCOES=(-o ClearAllForwardings=yes -o LogLevel=ERROR)
RAIZ="$(cd "$(dirname "$0")/.." && pwd)"

echo "→ copiando $RAIZ para $LAB:~/$DESTINO"
tar czf - -C "$RAIZ" \
	--exclude=.git --exclude=bin --exclude=.terraform --exclude='*.tfstate*' \
	--exclude=relatorio-floci.txt --exclude=client-go-validacao.log . |
	ssh "${SSH_OPCOES[@]}" "$LAB" "rm -rf ~/'$DESTINO' && mkdir -p ~/'$DESTINO' && tar xzf - -C ~/'$DESTINO'"

echo "→ validando no lab (a saída completa fica no relatório)"
# -t não: o script escreve no relatório e mostra só as linhas de resumo
set +e
ssh "${SSH_OPCOES[@]}" "$LAB" "cd ~/'$DESTINO' && bash scripts/validar-floci.sh" | grep -E '^(===|OK|FALHA|TUDO|[0-9]+ verifica)'
set -e

echo "→ trazendo o relatório e o go.sum"
scp -q "${SSH_OPCOES[@]}" "$LAB:~/$DESTINO/relatorio-floci.txt" "$RAIZ/relatorio-floci.txt"
# o go.sum foi gerado no lab com a verificação do sum.golang.org; traz para o projeto
scp -q "${SSH_OPCOES[@]}" "$LAB:~/$DESTINO/go.sum" "$RAIZ/go.sum" 2>/dev/null || echo "(go.sum não foi gerado)"

echo "pronto: $RAIZ/relatorio-floci.txt"
