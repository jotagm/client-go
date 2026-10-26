# Tópico do Client_GO e as duas filas (com DLQ). Mesmo módulo e mesmas políticas do ambiente
# Floci que o validar-floci.sh exercita; só o prefixo muda.
module "mensageria" {
  source  = "../../modules/mensageria"
  prefixo = "aviso-prod"

  filas = {
    # Código de login: retenção de 15 min (o código vale 10; depois não há o que entregar).
    "email-alta" = {
      filtro            = { canal = ["email"], prioridade = ["alta"] }
      retencao_segundos = 900
    }
    "email" = {
      filtro            = { canal = ["email"], prioridade = ["normal"] }
      retencao_segundos = 345600 # 4 dias
    }
  }
}
