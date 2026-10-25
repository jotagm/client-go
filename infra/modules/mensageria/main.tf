# Tópico do Client_GO e uma fila por (canal, prioridade), cada uma com DLQ.
#
# As filter policies precisam ser DISJUNTAS: uma notificação que case com duas assinaturas
# chega em duas filas e é processada duas vezes (o estado no banco segura a entrega dupla,
# mas é trabalho jogado fora).

resource "aws_sns_topic" "notificacoes" {
  name = "${var.prefixo}-notificacoes"
}

resource "aws_sqs_queue" "dlq" {
  for_each                  = var.filas
  name                      = "${var.prefixo}-${each.key}-dlq"
  message_retention_seconds = 1209600 # 14 dias para investigar
  sqs_managed_sse_enabled   = true
}

resource "aws_sqs_queue" "fila" {
  for_each = var.filas
  name     = "${var.prefixo}-${each.key}"

  # Precisa ser maior que o tempo de processar uma mensagem (render + chamada ao provedor).
  visibility_timeout_seconds = 30
  message_retention_seconds  = each.value.retencao_segundos
  # Long polling também do lado da fila: um ReceiveMessage sem WaitTimeSeconds não volta vazio na hora.
  receive_wait_time_seconds = 20
  # A mensagem carrega as variáveis (o código do OTP): criptografada em repouso.
  sqs_managed_sse_enabled = true

  # A retentativa do Client_GO desiste antes (5 tentativas, ver aplicacao.RetentativaPadrao).
  # A redrive é a rede de segurança para o que o Client_GO não consegue nem processar.
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq[each.key].arn
    maxReceiveCount     = var.max_recebimentos
  })
}

resource "aws_sqs_queue_policy" "permite_sns" {
  for_each  = var.filas
  queue_url = aws_sqs_queue.fila[each.key].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "sns.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.fila[each.key].arn
      Condition = { ArnEquals = { "aws:SourceArn" = aws_sns_topic.notificacoes.arn } }
    }]
  })
}

resource "aws_sns_topic_subscription" "assinatura" {
  for_each  = var.filas
  topic_arn = aws_sns_topic.notificacoes.arn
  protocol  = "sqs"
  endpoint  = aws_sqs_queue.fila[each.key].arn

  # Sem o envelope do SNS: o corpo na fila é exatamente o que o Client_GO publicou.
  raw_message_delivery = true
  filter_policy        = jsonencode(each.value.filtro)
}
