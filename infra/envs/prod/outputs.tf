# Os quatro primeiros viram variáveis do repositório jotagm/client-go no GitHub
# (Settings > Secrets and variables > Actions > Variables), com estes nomes.

output "AWS_ROLE_ARN" {
  value = aws_iam_role.github_deploy.arn
}

output "AWS_REGION" {
  value = var.regiao
}

output "ECR_REPOSITORY" {
  value = aws_ecr_repository.client_go.repository_url
}

output "EC2_INSTANCE_ID" {
  value = var.ec2_instance_id
}

output "DOCKER_PLATFORM" {
  description = "A EC2 do Booklit é t4g (arm64)"
  value       = "linux/arm64"
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
