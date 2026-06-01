terraform {
  required_version = ">= 1.5.0, < 1.16.0"

  backend "gcs" {}

  required_providers {
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 4.2"
    }
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "~> 7.0"
    }
    vault = {
      source  = "hashicorp/vault"
      version = "~> 5.7"
    }
  }
}
