variable "prefixo" {
  type        = string
  description = "Prefixo dos nomes (aviso, aviso-prod)."
}

variable "filas" {
  description = "Uma fila por chave. filtro é a filter policy da assinatura (atributos canal e prioridade)."
  type = map(object({
    filtro            = map(list(string))
    retencao_segundos = number
  }))
}

variable "max_recebimentos" {
  type        = number
  default     = 10
  description = "Recebimentos antes da DLQ. Fica acima do limite de tentativas do Client_GO (5)."
}
