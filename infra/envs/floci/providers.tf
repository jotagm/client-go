terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
  }
}

# Mesmo Floci do Booklit, em localhost:4566.
provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true

  endpoints {
    sns = var.endpoint
    sqs = var.endpoint
    sts = var.endpoint
  }
}

variable "endpoint" {
  type    = string
  default = "http://localhost:4566"
}
