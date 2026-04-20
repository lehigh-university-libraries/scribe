terraform {
  required_providers {
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 3.6"
    }
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
  }
}
