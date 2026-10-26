terraform {
  required_version = ">= 1.6"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.6"
    }
  }

  # Estado local por enquanto (fora do git pelo .gitignore). Ele guarda a senha do banco e o
  # token gerados: faça backup do terraform.tfstate.
}

provider "aws" {
  region = var.regiao

  default_tags {
    tags = {
      Projeto       = "client-go"
      Ambiente      = "prod"
      GerenciadoPor = "terraform"
    }
  }
}
