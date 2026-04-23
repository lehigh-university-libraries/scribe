terraform {
  required_providers {
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 4.2"
    }
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
  }
}
