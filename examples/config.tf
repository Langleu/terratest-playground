################################
# Magic Variables             # 
################################

locals {
  name = "lars" # just some abbotrary name to prefix resources
  # For demenstration purposes, we will use owner and acceptor as separation. Naming choice will become clearer when seeing the peering setup
  owner = {
    region           = "eu-west-2"     # London
    vpc_cidr_block   = "10.192.0.0/16" # vpc for the cluster and pod range
    region_full_name = "london"
  }
  accepter = {
    region           = "eu-west-3"     # Paris
    vpc_cidr_block   = "10.202.0.0/16" # vpc for the cluster and pod range
    region_full_name = "paris"
  }
}

################################
# Backend & Provider Setup    # 
################################

terraform {
  backend "local" {
    path = "terraform.tfstate"
  }

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "5.22.0"
    }
    kind = {
      source = "tehcyx/kind"
      version = "0.2.1"
    }
  }
}

provider "kind" {}

resource "kind_cluster" "default" {
    name = "${local.name}-${local.owner.region_full_name}"
    kubeconfig_path = "./kubeconfig"
    wait_for_ready = true
}
