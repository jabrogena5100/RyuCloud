-- +goose Up
-- Phase 3: asynchronous video processing.
--
-- Video lifecycle (status values stay lower-case to keep the API contract):
--   draft      = PENDING_UPLOAD   metadata only, no file yet
--   uploading  = UPLOADING        presigned URL issued, file not confirmed
--   queued     = QUEUED           file verified, waiting for a worker (NEW)
--   processing = PROCESSING       a worker holds the job
--   ready      = READY            HLS + thumbnail stored, playable
--   failed     = FAILED           upload or processing failed for good
ALTER TABLE videos DROP CONSTRAINT ck_videos_status_valid;
ALTER TABLE videos ADD CONSTRAINT ck_videos_status_valid
    CHECK (status IN ('draft', 'uploading', 'queued', 'processing', 'ready', 'failed'));
ALTER TABLE videos ADD COLUMN processing_error varchar(500);

-- One durable job per video. PostgreSQL is the source of truth for job state, leases
-- and retries; Redis only delivers "look at job X" messages to workers.
CREATE TABLE processing_jobs (
    id                uuid          NOT NULL DEFAULT gen_random_uuid(),
    video_id          uuid          NOT NULL,
    upload_id         uuid          NOT NULL,
    source_object_key varchar(1024) NOT NULL,
    state             varchar(20)   NOT NULL DEFAULT 'queued',
    stage             varchar(30)   NOT NULL DEFAULT 'queued',
    progress          smallint      NOT NULL DEFAULT 0,
    attempts          integer       NOT NULL DEFAULT 0,
    max_attempts      integer       NOT NULL DEFAULT 4,
    next_run_at       timestamptz   NOT NULL DEFAULT now(),
    locked_by         varchar(100),
    lease_expires_at  timestamptz,
    last_published_at timestamptz,
    last_error        varchar(1000),
    started_at        timestamptz,
    finished_at       timestamptz,
    created_at        timestamptz   NOT NULL DEFAULT now(),
    updated_at        timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT pk_processing_jobs PRIMARY KEY (id),
    CONSTRAINT uq_processing_jobs_video_id UNIQUE (video_id),
    CONSTRAINT uq_processing_jobs_upload_id UNIQUE (upload_id),
    CONSTRAINT fk_processing_jobs_video_id_videos FOREIGN KEY (video_id) REFERENCES videos (id) ON DELETE CASCADE,
    CONSTRAINT fk_processing_jobs_upload_id_video_uploads FOREIGN KEY (upload_id) REFERENCES video_uploads (id) ON DELETE CASCADE,
    CONSTRAINT ck_processing_jobs_state_valid CHECK (state IN ('queued', 'processing', 'retry', 'succeeded', 'failed')),
    CONSTRAINT ck_processing_jobs_progress_range CHECK (progress BETWEEN 0 AND 100),
    CONSTRAINT ck_processing_jobs_attempts_bounded CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts),
    -- A job being processed always has an owner and a lease deadline.
    CONSTRAINT ck_processing_jobs_processing_has_lease CHECK (
        state <> 'processing' OR (locked_by IS NOT NULL AND lease_expires_at IS NOT NULL)
    )
);
CREATE INDEX ix_processing_jobs_state_next_run ON processing_jobs (state, next_run_at);

-- One row per generated HLS rendition (the master playlist and thumbnail keys live on videos).
CREATE TABLE video_renditions (
    id            uuid          NOT NULL DEFAULT gen_random_uuid(),
    video_id      uuid          NOT NULL,
    name          varchar(20)   NOT NULL,
    width         integer       NOT NULL,
    height        integer       NOT NULL,
    bandwidth_bps bigint        NOT NULL,
    segment_count integer       NOT NULL,
    size_bytes    bigint        NOT NULL,
    playlist_key  varchar(1024) NOT NULL,
    created_at    timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT pk_video_renditions PRIMARY KEY (id),
    CONSTRAINT uq_video_renditions_video_id_name UNIQUE (video_id, name),
    CONSTRAINT fk_video_renditions_video_id_videos FOREIGN KEY (video_id) REFERENCES videos (id) ON DELETE CASCADE,
    CONSTRAINT ck_video_renditions_positive CHECK (width > 0 AND height > 0 AND segment_count > 0 AND size_bytes >= 0)
);

-- Videos that Phase 2 left verified-and-waiting ("processing" meant "no worker yet")
-- get a job now, so nothing already uploaded is stranded.
INSERT INTO processing_jobs (video_id, upload_id, source_object_key)
SELECT v.id, u.id, v.source_object_key
FROM videos v
JOIN video_uploads u ON u.video_id = v.id AND u.status = 'uploaded'
WHERE v.status = 'processing' AND v.source_object_key IS NOT NULL;

UPDATE videos SET status = 'queued', updated_at = now()
WHERE status = 'processing' AND id IN (SELECT video_id FROM processing_jobs);

-- +goose Down
UPDATE videos SET status = 'processing', updated_at = now() WHERE status = 'queued';
DROP TABLE video_renditions;
DROP TABLE processing_jobs;
ALTER TABLE videos DROP COLUMN processing_error;
ALTER TABLE videos DROP CONSTRAINT ck_videos_status_valid;
ALTER TABLE videos ADD CONSTRAINT ck_videos_status_valid
    CHECK (status IN ('draft', 'uploading', 'processing', 'ready', 'failed'));
