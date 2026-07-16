# cloud-compose 0.6.x kept GCP resources directly under module.scribe.
# cloud-compose 1.2.x and 1.3.x move them under a counted module.scribe.module.gcp[0].
# The upstream module includes the first move, but not the counted-module
# instance hop Terraform needs for our existing prod/dev state.
moved {
  from = module.scribe.module.gcp.time_static.snapshot_time_static
  to   = module.scribe.module.gcp[0].time_static.snapshot_time_static
}

moved {
  from = module.scribe.module.gcp.google_service_account.cloud-compose
  to   = module.scribe.module.gcp[0].google_service_account.cloud-compose
}

moved {
  from = module.scribe.module.gcp.google_artifact_registry_repository_iam_member.private-policy-cloud-compose
  to   = module.scribe.module.gcp[0].google_artifact_registry_repository_iam_member.private-policy-cloud-compose
}

moved {
  from = module.scribe.module.gcp.google_project_iam_member.log
  to   = module.scribe.module.gcp[0].google_project_iam_member.log
}

moved {
  from = module.scribe.module.gcp.google_compute_disk.boot
  to   = module.scribe.module.gcp[0].google_compute_disk.boot
}

moved {
  from = module.scribe.module.gcp.google_compute_disk.data
  to   = module.scribe.module.gcp[0].google_compute_disk.data
}

moved {
  from = module.scribe.module.gcp.google_compute_disk.docker-volumes
  to   = module.scribe.module.gcp[0].google_compute_disk.docker-volumes
}

moved {
  from = module.scribe.module.gcp.google_compute_reservation.production
  to   = module.scribe.module.gcp[0].google_compute_reservation.production
}

moved {
  from = module.scribe.module.gcp.google_compute_resource_policy.daily_snapshot
  to   = module.scribe.module.gcp[0].google_compute_resource_policy.daily_snapshot
}

moved {
  from = module.scribe.module.gcp.google_compute_resource_policy.weekly_snapshot
  to   = module.scribe.module.gcp[0].google_compute_resource_policy.weekly_snapshot
}

moved {
  from = module.scribe.module.gcp.google_compute_disk_resource_policy_attachment.daily_snapshot
  to   = module.scribe.module.gcp[0].google_compute_disk_resource_policy_attachment.daily_snapshot
}

moved {
  from = module.scribe.module.gcp.google_compute_disk_resource_policy_attachment.weekly_snapshot
  to   = module.scribe.module.gcp[0].google_compute_disk_resource_policy_attachment.weekly_snapshot
}

moved {
  from = module.scribe.module.gcp.google_compute_disk.overlay_disk
  to   = module.scribe.module.gcp[0].google_compute_disk.overlay_disk
}

moved {
  from = module.scribe.module.gcp.google_compute_instance.cloud-compose
  to   = module.scribe.module.gcp[0].google_compute_instance.cloud-compose
}

moved {
  from = module.scribe.module.gcp.google_service_account.app
  to   = module.scribe.module.gcp[0].google_service_account.app[0]
}

moved {
  from = module.scribe.module.gcp.google_service_account.ppb
  to   = module.scribe.module.gcp[0].google_service_account.ppb[0]
}

moved {
  from = module.scribe.module.gcp.module.ppb
  to   = module.scribe.module.gcp[0].module.ppb[0]
}

moved {
  from = module.scribe.module.gcp.google_compute_firewall.allow_ssh_ipv4
  to   = module.scribe.module.gcp[0].google_compute_firewall.allow_ssh_ipv4
}

moved {
  from = module.scribe.module.gcp.google_compute_firewall.allow_ssh_ipv6
  to   = module.scribe.module.gcp[0].google_compute_firewall.allow_ssh_ipv6
}

moved {
  from = module.scribe.module.gcp.google_compute_firewall.allow_rollout_ipv4
  to   = module.scribe.module.gcp[0].google_compute_firewall.allow_rollout_ipv4
}
