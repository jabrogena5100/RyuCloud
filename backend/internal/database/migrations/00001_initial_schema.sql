-- +goose Up
-- Initial schema: users, auth sessions, channels and video metadata.
CREATE TABLE users (
    id            uuid         NOT NULL DEFAULT gen_random_uuid(),
    email         varchar(320) NOT NULL,
    password_hash varchar(255) NOT NULL,
    display_name  varchar(100) NOT NULL,
    is_active     boolean      NOT NULL DEFAULT true,
    created_at    timestamptz  NOT NULL DEFAULT now(),
    updated_at    timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT pk_users PRIMARY KEY (id),
    CONSTRAINT uq_users_email UNIQUE (email),
    CONSTRAINT ck_users_email_lowercase CHECK (email = lower(email))
);

-- Server-side login sessions. Only the SHA-256 of the cookie token is stored.
CREATE TABLE auth_sessions (
    id         uuid        NOT NULL DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL,
    token_hash varchar(64) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    CONSTRAINT pk_auth_sessions PRIMARY KEY (id),
    CONSTRAINT uq_auth_sessions_token_hash UNIQUE (token_hash),
    CONSTRAINT fk_auth_sessions_user_id_users FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);
CREATE INDEX ix_auth_sessions_user_id ON auth_sessions (user_id);

CREATE TABLE channels (
    id          uuid         NOT NULL DEFAULT gen_random_uuid(),
    owner_id    uuid         NOT NULL,
    handle      varchar(30)  NOT NULL,
    name        varchar(100) NOT NULL,
    description text         NOT NULL DEFAULT '',
    created_at  timestamptz  NOT NULL DEFAULT now(),
    updated_at  timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT pk_channels PRIMARY KEY (id),
    CONSTRAINT uq_channels_handle UNIQUE (handle),
    CONSTRAINT fk_channels_owner_id_users FOREIGN KEY (owner_id) REFERENCES users (id) ON DELETE CASCADE
);
CREATE INDEX ix_channels_owner_id ON channels (owner_id);

-- Object-storage keys (not URLs) are filled in by later phases: the raw upload,
-- the HLS master playlist written by the worker, and the generated thumbnail.
CREATE TABLE videos (
    id                   uuid          NOT NULL DEFAULT gen_random_uuid(),
    channel_id           uuid          NOT NULL,
    title                varchar(100)  NOT NULL,
    description          text          NOT NULL DEFAULT '',
    status               varchar(20)   NOT NULL DEFAULT 'draft',
    visibility           varchar(20)   NOT NULL DEFAULT 'private',
    duration_seconds     integer,
    view_count           bigint        NOT NULL DEFAULT 0,
    source_object_key    varchar(1024),
    hls_manifest_key     varchar(1024),
    thumbnail_object_key varchar(1024),
    published_at         timestamptz,
    created_at           timestamptz   NOT NULL DEFAULT now(),
    updated_at           timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT pk_videos PRIMARY KEY (id),
    CONSTRAINT fk_videos_channel_id_channels FOREIGN KEY (channel_id) REFERENCES channels (id) ON DELETE CASCADE,
    CONSTRAINT ck_videos_status_valid CHECK (status IN ('draft', 'uploading', 'processing', 'ready', 'failed')),
    CONSTRAINT ck_videos_visibility_valid CHECK (visibility IN ('public', 'unlisted', 'private')),
    CONSTRAINT ck_videos_duration_non_negative CHECK (duration_seconds IS NULL OR duration_seconds >= 0)
);
CREATE INDEX ix_videos_channel_id ON videos (channel_id);
CREATE INDEX ix_videos_feed ON videos (visibility, status, published_at);

-- +goose Down
DROP TABLE videos;
DROP TABLE channels;
DROP TABLE auth_sessions;
DROP TABLE users;
