terraform {
  required_version = ">= 1.8"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # Local state, kept outside this repo: `just infra-init` points it at
  # $DEVBOX_OPS/state (see the README). It holds instance details, so never
  # commit it here.
  backend "local" {}
}

provider "aws" {
  region  = var.region
  profile = var.aws_profile

  default_tags {
    tags = {
      Project   = "devbox"
      Owner     = var.owner
      ManagedBy = "opentofu"
    }
  }
}
