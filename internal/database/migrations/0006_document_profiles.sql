ALTER TABLE contexts ADD COLUMN document_type VARCHAR(64) NOT NULL DEFAULT 'generic';
ALTER TABLE items ADD COLUMN document_type VARCHAR(64) NULL;
UPDATE contexts SET document_type = 'newspaper' WHERE segmentation_model = 'newspapers';
