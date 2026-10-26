resource "aws_ecr_repository" "client_go" {
  name                 = "client-go"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }
}

# Guarda as 10 imagens mais recentes: dá para voltar versões sem pagar armazenamento à toa.
resource "aws_ecr_lifecycle_policy" "client_go" {
  repository = aws_ecr_repository.client_go.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Mantem as 10 imagens mais recentes"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 10
      }
      action = { type = "expire" }
    }]
  })
}
