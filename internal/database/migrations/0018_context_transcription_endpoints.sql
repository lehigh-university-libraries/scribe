ALTER TABLE contexts
  ADD COLUMN IF NOT EXISTS transcription_base_url VARCHAR(2048) NULL AFTER transcription_model,
  ADD COLUMN IF NOT EXISTS transcription_audience VARCHAR(2048) NULL AFTER transcription_base_url;
