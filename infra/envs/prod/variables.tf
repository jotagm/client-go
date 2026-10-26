variable "regiao" {
  type    = string
  default = "us-east-1"
}

# --- Vindo da infra do Booklit (infra/envs/prod do booklit-server) --------------------------
# O Client_GO não cria máquina nem banco: roda como terceiro container na EC2 do Booklit e usa
# um database próprio no RDS dele. Preencha com `terraform output` de lá.

variable "ec2_instance_id" {
  description = "ID da EC2 do Booklit (saída EC2_INSTANCE_ID de lá)"
  type        = string
}

variable "ec2_role_name" {
  description = "Papel IAM da EC2 do Booklit. Os dois containers compartilham o papel da instância"
  type        = string
  default     = "booklit-prod-api"
}

variable "rds_endpoint" {
  description = "Endereço do RDS do Booklit (saída banco_endpoint de lá), sem a porta"
  type        = string
}

# --- Do Client_GO ------------------------------------------------------------------------------

variable "github_repo" {
  description = "Repositório autorizado a fazer deploy (dono/nome)"
  type        = string
  default     = "jotagm/client-go"
}

variable "dominio_email" {
  description = "Domínio já verificado no SES pelo Terraform do Booklit (email.tf)"
  type        = string
  default     = "booklit.bio"
}

variable "remetente" {
  description = "Remetente dos e-mails. Precisa ser um endereço do domínio verificado; sem isso o SES responde MessageRejected"
  type        = string
  default     = "Booklit <no-reply@booklit.bio>"
}

variable "workers" {
  description = "Workers por fila. Cada um é um long polling (~130 mil requisições SQS por mês); 2 cabe no nível gratuito"
  type        = number
  default     = 2
}
