-- Private resumable work. The empty line ID holds the segmented page once;
-- other rows hold individual completed line annotations. Repository
-- transactions delete these children when jobs terminate or are removed.
CREATE TABLE transcription_job_checkpoints (
  job_id BIGINT UNSIGNED NOT NULL,
  line_id VARBINARY(2048) NOT NULL,
  payload LONGTEXT NOT NULL,
  PRIMARY KEY (job_id, line_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
