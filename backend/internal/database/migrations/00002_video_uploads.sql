-- +goose Up
-- Phase 2: one row per upload attempt. The browser uploads straight to object
-- storage; this table tracks the attempt's lifecycle and what the API verified.
--
-- status: pending   -> a presigned URL was issued, the file is not confirmed yet
--         uploaded  -> the API confirmed the object exists with the declared size/type
--         failed    -> verification failed (size/type mismatch)
--         cancelled -> the owner cancelled before completion
--         expired   -> the upload window elapsed without confirmation
CREATE TABLE video_uploads (
    id                  uuid          NOT NULL DEFAULT gen_random_uuid(),
    video_id            uuid          NOT NULL,
    user_id             uuid          NOT NULL,
    object_key          varchar(1024) NOT NULL,
    original_filename   varchar(255)  NOT NULL,
    content_type        varchar(100)  NOT NULL,
    declared_size_bytes bigint        NOT NULL,
    verified_size_bytes bigint,
    etag                varchar(200),
    status              varchar(20)   NOT NULL DEFAULT 'pending',
    failure_reason      varchar(500),
    expires_at          timestamptz   NOT NULL,
    completed_at        timestamptz,
    created_at          timestamptz   NOT NULL DEFAULT now(),
    updated_at          timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT pk_video_uploads PRIMARY KEY (id),
    CONSTRAINT uq_video_uploads_object_key UNIQUE (object_key),
    CONSTRAINT fk_video_uploads_video_id_videos FOREIGN KEY (video_id) REFERENCES videos (id) ON DELETE CASCADE,
    CONSTRAINT fk_video_uploads_user_id_users FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT ck_video_uploads_status_valid CHECK (status IN ('pending', 'uploaded', 'failed', 'cancelled', 'expired')),
    CONSTRAINT ck_video_uploads_declared_size_positive CHECK (declared_size_bytes > 0),
    -- An upload can only be "uploaded" once the API verified a matching object.
    CONSTRAINT ck_video_uploads_uploaded_is_verified CHECK (
        status <> 'uploaded'
        OR (verified_size_bytes IS NOT NULL AND completed_at IS NOT NULL AND verified_size_bytes = declared_size_bytes)
    )
);
CREATE INDEX ix_video_uploads_user_status ON video_uploads (user_id, status);
CREATE INDEX ix_video_uploads_video_id ON video_uploads (video_id);
-- At most one in-flight upload per video.
CREATE UNIQUE INDEX ux_video_uploads_one_pending_per_video ON video_uploads (video_id) WHERE status = 'pending';

-- +goose Down
DROP TABLE video_uploads;
