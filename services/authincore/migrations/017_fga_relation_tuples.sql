-- +goose Up

CREATE TABLE relation_tuples (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    object_type      TEXT NOT NULL,
    object_id        TEXT NOT NULL,
    relation         TEXT NOT NULL,
    subject_type     TEXT NOT NULL,
    subject_id       TEXT NOT NULL,
    subject_relation TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (org_id, object_type, object_id, relation, subject_type, subject_id, subject_relation)
);

-- Fast check: "does user X have relation R on object Y?"
CREATE INDEX idx_rt_check ON relation_tuples
    (org_id, object_type, object_id, relation, subject_type, subject_id);

-- Fast list-objects: "which objects of type T can user X access?"
CREATE INDEX idx_rt_list_objects ON relation_tuples
    (org_id, object_type, relation, subject_type, subject_id);

-- Fast list-subjects: "who has relation R on object Y?"
CREATE INDEX idx_rt_list_subjects ON relation_tuples
    (org_id, object_type, object_id, relation);

-- Fast cascade delete when a subject (user) is deleted
CREATE INDEX idx_rt_subject ON relation_tuples
    (org_id, subject_type, subject_id);

-- +goose Down
DROP TABLE IF EXISTS relation_tuples;
