# cloud-compose 1.8.1 can consume an existing network and subnet. Preserve the
# exact deployed objects while transferring only their Terraform ownership to
# the root configuration; no VPC or application subnet is recreated. Keep this
# file moved-block-only so the state-only normalizer can process it as its final
# phase after historical upstream and counted-module address migrations.
moved {
  from = module.scribe.module.gcp[0].google_compute_network.cloud-compose[0]
  to   = google_compute_network.application
}

moved {
  from = module.scribe.module.gcp[0].google_compute_subnetwork.cloud-compose[0]
  to   = google_compute_subnetwork.application
}
