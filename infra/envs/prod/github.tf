# GitHub Actions do repositório do Client_GO entra na AWS por OIDC, como no Booklit. O provedor
# OIDC é um por conta e já foi criado pelo Terraform do Booklit: aqui só se lê.

data "aws_iam_openid_connect_provider" "github" {
  url = "https://token.actions.githubusercontent.com"
}

locals {
  # O GitHub manda o formato antigo (só nomes) em repositórios antigos e o imutável (nomes + IDs)
  # nos criados depois de 15/07/2026. Uma lista no StringEquals é um OU: qualquer um dos dois.
  github_subs = compact([
    "repo:${var.github_repo}:ref:refs/heads/main",
    var.github_sub_imutavel,
  ])
}

resource "aws_iam_role" "github_deploy" {
  name = "client-go-prod-github-deploy"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = data.aws_iam_openid_connect_provider.github.arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com"
          # Só a main deste repositório: PR, fork ou outra branch não assumem o papel.
          "token.actions.githubusercontent.com:sub" = local.github_subs
        }
      }
    }]
  })
}

resource "aws_iam_role_policy" "github_deploy" {
  name = "client-go-prod-github-deploy"
  role = aws_iam_role.github_deploy.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "LoginNoEcr"
        Effect   = "Allow"
        Action   = ["ecr:GetAuthorizationToken"]
        Resource = "*"
      },
      {
        Sid    = "PushDaImagem"
        Effect = "Allow"
        Action = [
          "ecr:BatchCheckLayerAvailability", "ecr:InitiateLayerUpload", "ecr:UploadLayerPart",
          "ecr:CompleteLayerUpload", "ecr:PutImage", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer",
        ]
        Resource = aws_ecr_repository.client_go.arn
      },
      {
        Sid    = "DeployNaEc2"
        Effect = "Allow"
        Action = ["ssm:SendCommand"]
        Resource = [
          "arn:aws:ec2:${var.regiao}:${local.conta}:instance/${var.ec2_instance_id}",
          "arn:aws:ssm:${var.regiao}::document/AWS-RunShellScript",
        ]
      },
      {
        Sid      = "AcompanharDeploy"
        Effect   = "Allow"
        Action   = ["ssm:GetCommandInvocation", "ssm:ListCommandInvocations"]
        Resource = "*"
      },
    ]
  })
}
