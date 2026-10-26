# Configuração do Client_GO no SSM, em /client-go/prod/<VARIAVEL>. O deploy.sh da EC2 lê tudo
# deste caminho e monta o app.env do container, igual ao que o Booklit faz com /booklit/prod.

locals {
  prefixo_ssm         = "/client-go/prod"
  prefixo_ssm_booklit = "/booklit/prod"
  conta               = data.aws_caller_identity.atual.account_id
}

data "aws_caller_identity" "atual" {}

# Mesma regra do Booklit: 48 caracteres alfanuméricos (~285 bits), sem caracteres que
# precisem de escape numa URL de banco ou num cabeçalho.
resource "random_password" "banco" {
  length  = 32
  special = false
}

resource "random_password" "token_booklit" {
  length  = 48
  special = false
}

resource "aws_ssm_parameter" "configuracao" {
  for_each = {
    AWS_REGION          = var.regiao
    AVISO_ENDERECO      = ":8081"
    AVISO_PROVEDOR      = "ses"
    AVISO_SES_REMETENTE = var.remetente
    AVISO_TOPICO_ARN    = module.mensageria.topico_arn
    AVISO_FILAS         = module.mensageria.aviso_filas
    AVISO_WORKERS       = tostring(var.workers)
  }

  name  = "${local.prefixo_ssm}/${each.key}"
  type  = "String"
  value = each.value
}

resource "aws_ssm_parameter" "segredo" {
  # Lista literal: as chaves do for_each não podem derivar de valores sensíveis.
  for_each = toset(["AVISO_DB_URL", "AVISO_TOKENS"])

  name = "${local.prefixo_ssm}/${each.key}"
  type = "SecureString"
  value = {
    # O database "aviso" e o usuário "aviso" são criados uma vez por scripts/criar-banco-prod.sh
    # (o RDS não tem IP público; o Terraform daqui não alcança o Postgres).
    AVISO_DB_URL = "postgres://aviso:${random_password.banco.result}@${var.rds_endpoint}:5432/aviso?sslmode=require"
    AVISO_TOKENS = "booklit=${random_password.token_booklit.result}"
  }[each.key]
}

# --- Lado do Booklit ---------------------------------------------------------------------------
# O Booklit lê /booklit/prod no deploy dele. Estes três parâmetros ficam só aqui (o Terraform do
# Booklit não os conhece), então um não pisa no outro.

resource "aws_ssm_parameter" "booklit_url" {
  name  = "${local.prefixo_ssm_booklit}/BOOKLIT_CLIENT_GO_URL"
  type  = "String"
  value = "http://client-go:8081" # nome do container na rede do compose do Booklit
}

resource "aws_ssm_parameter" "booklit_token" {
  name  = "${local.prefixo_ssm_booklit}/BOOKLIT_CLIENT_GO_TOKEN"
  type  = "SecureString"
  value = random_password.token_booklit.result
}

# A chave da virada: "ses" (envio direto, o de hoje) ou "client-go". Nasce em "ses" e o Terraform
# não a toca mais: a troca é um `aws ssm put-parameter --overwrite` seguido de um deploy do
# Booklit (ver docs/producao.md). Voltar é o mesmo comando com "ses".
resource "aws_ssm_parameter" "booklit_provedor" {
  name  = "${local.prefixo_ssm_booklit}/BOOKLIT_EMAIL_PROVEDOR"
  type  = "String"
  value = "ses"

  lifecycle {
    ignore_changes = [value]
  }
}
