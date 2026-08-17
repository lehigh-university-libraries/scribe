locals {
  readiness_jobs = {
    backend = try(google_cloud_run_v2_job.backend_readiness[0].name, "")
    browser = try(google_cloud_run_v2_job.browser_readiness[0].name, "")
    ocr     = try(google_cloud_run_v2_job.ocr_readiness[0].name, "")
  }
}

check "production_notification_channels_configured" {
  assert {
    condition     = !local.is_prod_workspace || length(var.monitoring_notification_channels) > 0
    error_message = "Production requires at least one Cloud Monitoring notification channel. Set MONITORING_NOTIFICATION_CHANNELS in the protected production environment."
  }
}

resource "google_monitoring_alert_policy" "transcription_queue_age" {
  count = local.is_prod_workspace ? 1 : 0

  display_name          = "${var.name} ${local.workspace_slug} transcription queue is stalled"
  combiner              = "OR"
  notification_channels = var.monitoring_notification_channels

  documentation {
    content   = "The oldest unacked transcription message exceeded 15 minutes. Inspect worker readiness, leases, provider errors, and the dead-letter subscription."
    mime_type = "text/markdown"
  }

  conditions {
    display_name = "Oldest transcription message exceeds 15 minutes"
    condition_threshold {
      filter          = "resource.type = \"pubsub_subscription\" AND resource.labels.subscription_id = \"${google_pubsub_subscription.transcription_workers.name}\" AND metric.type = \"pubsub.googleapis.com/subscription/oldest_unacked_message_age\""
      comparison      = "COMPARISON_GT"
      threshold_value = 900
      duration        = "300s"
      aggregations {
        alignment_period   = "60s"
        per_series_aligner = "ALIGN_MAX"
      }
    }
  }
}

resource "google_monitoring_alert_policy" "transcription_queue_age_forward" {
  for_each = local.forward_production_transcription_data_generations

  display_name          = "${var.name} ${local.workspace_slug} ${each.key} transcription queue is stalled"
  combiner              = "OR"
  notification_channels = var.monitoring_notification_channels

  documentation {
    content   = "The oldest unacked ${each.key} transcription message exceeded 15 minutes. Inspect worker readiness, leases, provider errors, and the dead-letter subscription."
    mime_type = "text/markdown"
  }

  conditions {
    display_name = "${each.key} oldest transcription message exceeds 15 minutes"
    condition_threshold {
      filter          = "resource.type = \"pubsub_subscription\" AND resource.labels.subscription_id = \"${google_pubsub_subscription.transcription_workers_forward[each.key].name}\" AND metric.type = \"pubsub.googleapis.com/subscription/oldest_unacked_message_age\""
      comparison      = "COMPARISON_GT"
      threshold_value = 900
      duration        = "300s"
      aggregations {
        alignment_period   = "60s"
        per_series_aligner = "ALIGN_MAX"
      }
    }
  }
}

resource "google_monitoring_alert_policy" "frontend_server_errors" {
  count = local.is_prod_workspace ? 1 : 0

  display_name          = "${var.name} ${local.workspace_slug} frontend ingress 5xx responses"
  combiner              = "OR"
  notification_channels = var.monitoring_notification_channels

  documentation {
    content   = "The public Cloud Run ingress is returning server errors. Execute the backend, browser, and OCR readiness jobs and inspect the frontend proxy, VM, API, and database health."
    mime_type = "text/markdown"
  }

  conditions {
    display_name = "Frontend ingress returns 5xx responses"
    condition_threshold {
      filter          = "resource.type = \"cloud_run_revision\" AND resource.labels.service_name = \"${var.name}\" AND metric.type = \"run.googleapis.com/request_count\" AND metric.labels.response_code_class = \"5xx\""
      comparison      = "COMPARISON_GT"
      threshold_value = 0
      duration        = "300s"
      aggregations {
        alignment_period     = "300s"
        per_series_aligner   = "ALIGN_SUM"
        cross_series_reducer = "REDUCE_SUM"
        group_by_fields      = ["resource.labels.service_name"]
      }
    }
  }
}

resource "google_monitoring_alert_policy" "readiness_job_failures" {
  for_each = local.is_prod_workspace ? {
    for kind, job in local.readiness_jobs : kind => job if trimspace(job) != ""
  } : {}

  display_name          = "${each.value} failed"
  combiner              = "OR"
  notification_channels = var.monitoring_notification_channels

  documentation {
    content   = "A Scribe protected deep-readiness job failed. Treat its backend, browser, or OCR path as unavailable until a successful execution is recorded."
    mime_type = "text/markdown"
  }

  conditions {
    display_name = "Cloud Run readiness execution failed"
    condition_threshold {
      filter          = "resource.type = \"cloud_run_job\" AND resource.labels.job_name = \"${each.value}\" AND metric.type = \"run.googleapis.com/job/completed_execution_count\" AND metric.labels.result = \"failed\""
      comparison      = "COMPARISON_GT"
      threshold_value = 0
      duration        = "0s"
      aggregations {
        alignment_period     = "300s"
        per_series_aligner   = "ALIGN_SUM"
        cross_series_reducer = "REDUCE_SUM"
        group_by_fields      = ["resource.labels.job_name"]
      }
    }
  }
}

resource "google_monitoring_alert_policy" "persistent_disk_utilization" {
  count = local.is_prod_workspace ? 1 : 0

  display_name          = "${var.name} ${local.workspace_slug} persistent disk capacity"
  combiner              = "OR"
  notification_channels = var.monitoring_notification_channels

  documentation {
    content   = "A production filesystem exceeded 80% utilization, or disk-utilization telemetry disappeared. The cloud-compose data disk must retain capacity for application data, the completed logical MariaDB dump, and a full staging dump."
    mime_type = "text/markdown"
  }

  conditions {
    display_name = "Persistent filesystem is over 80 percent full"
    condition_threshold {
      filter                  = "resource.type = \"gce_instance\" AND resource.labels.instance_id = \"${module.scribe.instance.id}\" AND metric.type = \"agent.googleapis.com/disk/percent_used\""
      comparison              = "COMPARISON_GT"
      threshold_value         = 80
      duration                = "300s"
      evaluation_missing_data = "EVALUATION_MISSING_DATA_ACTIVE"
      aggregations {
        alignment_period     = "300s"
        per_series_aligner   = "ALIGN_MAX"
        cross_series_reducer = "REDUCE_MAX"
      }
    }
  }
}
