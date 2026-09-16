CREATE TABLE content_filters (
    id bigserial PRIMARY KEY,
    keyword text NOT NULL,
    block_ingest boolean NOT NULL DEFAULT false,
    copyright_restricted boolean NOT NULL DEFAULT false,
    sensitive boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT content_filters_keyword_length
        CHECK (char_length(btrim(keyword)) >= 2),
    CONSTRAINT content_filters_has_action
        CHECK (block_ingest OR copyright_restricted OR sensitive)
);

CREATE UNIQUE INDEX content_filters_keyword_normalized_unique
    ON content_filters ((lower(btrim(keyword))));
