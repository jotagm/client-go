output "topico_arn" {
  value = aws_sns_topic.notificacoes.arn
}

output "filas_url" {
  value = { for nome, fila in aws_sqs_queue.fila : nome => fila.id }
}

output "dlqs_url" {
  value = { for nome, fila in aws_sqs_queue.dlq : nome => fila.id }
}

# Pronto para colar na variável AVISO_FILAS.
output "aviso_filas" {
  value = join(",", [for nome, fila in aws_sqs_queue.fila : "${nome}=${fila.id}"])
}
