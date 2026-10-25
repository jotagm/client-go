module "mensageria" {
  source  = "../../modules/mensageria"
  prefixo = "aviso"

  filas = {
    # Código de login: não espera atrás de lembrete. Retenção de 15 min: o código vale 10,
    # depois disso a mensagem não tem mais o que entregar.
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

output "topico_arn" {
  value = module.mensageria.topico_arn
}

output "aviso_filas" {
  value = module.mensageria.aviso_filas
}

output "dlqs_url" {
  value = module.mensageria.dlqs_url
}
