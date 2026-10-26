# Os dois containers (Booklit e Client_GO) rodam na mesma EC2 e portanto assumem o MESMO papel
# da instância. Não há como separar permissões por container sem mudar para ECS/EKS. O que
# este arquivo faz é acrescentar ao papel do Booklit exatamente o que o Client_GO precisa, numa
# política própria: dá para ver e remover à parte.

data "aws_iam_role" "ec2" {
  name = var.ec2_role_name
}

resource "aws_iam_role_policy" "client_go" {
  name = "client-go-prod"
  role = data.aws_iam_role.ec2.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "LerConfiguracao"
        Effect = "Allow"
        Action = ["ssm:GetParametersByPath", "ssm:GetParameters", "ssm:GetParameter"]
        Resource = [
          "arn:aws:ssm:${var.regiao}:${local.conta}:parameter${local.prefixo_ssm}",
          "arn:aws:ssm:${var.regiao}:${local.conta}:parameter${local.prefixo_ssm}/*",
        ]
      },
      {
        Sid      = "PublicarNotificacoes"
        Effect   = "Allow"
        Action   = ["sns:Publish"]
        Resource = [module.mensageria.topico_arn]
      },
      {
        Sid    = "ConsumirFilas"
        Effect = "Allow"
        Action = [
          "sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility",
          "sqs:GetQueueAttributes", "sqs:GetQueueUrl",
        ]
        Resource = ["arn:aws:sqs:${var.regiao}:${local.conta}:aviso-prod-*"]
      },
      {
        # Declarado aqui de propósito: quando o Booklit largar o SesEnviadorDeOtp e perder o
        # ses:SendEmail da política dele, o Client_GO continua enviando.
        Sid      = "EnviarEmail"
        Effect   = "Allow"
        Action   = ["ses:SendEmail"]
        Resource = ["arn:aws:ses:${var.regiao}:${local.conta}:identity/${var.dominio_email}"]
      },
    ]
  })
}
