CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
CREATE TABLE workspaces (id uuid PRIMARY KEY, name text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE api_keys (id uuid PRIMARY KEY, name text NOT NULL, digest bytea UNIQUE NOT NULL, revoked_at timestamptz, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE key_permissions (key_id uuid REFERENCES api_keys(id), workspace_id uuid REFERENCES workspaces(id), can_write boolean NOT NULL, PRIMARY KEY(key_id,workspace_id));
CREATE TABLE objects (
 workspace_id uuid NOT NULL REFERENCES workspaces(id), id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('source','claim','verification','assessment','knowledge')),
 current_version integer NOT NULL CHECK(current_version>0), created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,id), UNIQUE(workspace_id,id,kind)
);
CREATE TABLE revisions (
 workspace_id uuid NOT NULL, object_id uuid NOT NULL, version integer NOT NULL CHECK(version>0),
 kind text NOT NULL, body text NOT NULL DEFAULT '', tags text[] NOT NULL DEFAULT '{}', conditions text NOT NULL DEFAULT '',
 metadata jsonb NOT NULL DEFAULT '{}', author uuid NOT NULL REFERENCES api_keys(id), reason text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), created_tx bigint NOT NULL DEFAULT txid_current(), provenance text NOT NULL DEFAULT '', source_time timestamptz,
 source_type text NOT NULL DEFAULT '', method text NOT NULL DEFAULT '', performed_at timestamptz,
 conclusion text CHECK(conclusion IN ('support','refute','uncertain')), valid_from timestamptz, valid_until timestamptz,
 claim_id uuid, claim_version integer,
 PRIMARY KEY(workspace_id,object_id,version),
 FOREIGN KEY(workspace_id,object_id,kind) REFERENCES objects(workspace_id,id,kind),
 FOREIGN KEY(workspace_id,claim_id,claim_version) REFERENCES revisions(workspace_id,object_id,version),
 CHECK((claim_id IS NULL)=(claim_version IS NULL)),
 CHECK(valid_from IS NULL OR valid_until IS NULL OR valid_from<=valid_until),
 CHECK(kind NOT IN ('verification','assessment') OR (version=1 AND claim_id IS NOT NULL AND conclusion IS NOT NULL)),
 CHECK(kind<>'verification' OR (method<>'' AND performed_at IS NOT NULL)),
 CHECK(kind<>'source' OR (provenance<>'' AND source_type<>''))
);
ALTER TABLE objects ADD FOREIGN KEY(workspace_id,id,current_version) REFERENCES revisions(workspace_id,object_id,version) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE links (
 workspace_id uuid NOT NULL, from_id uuid NOT NULL, from_version integer NOT NULL,
 role text NOT NULL CHECK(role IN ('source','evidence','verification','claim','assessment')),
 to_id uuid NOT NULL, to_version integer NOT NULL,
 PRIMARY KEY(workspace_id,from_id,from_version,role,to_id,to_version),
 FOREIGN KEY(workspace_id,from_id,from_version) REFERENCES revisions(workspace_id,object_id,version),
 FOREIGN KEY(workspace_id,to_id,to_version) REFERENCES revisions(workspace_id,object_id,version)
);
CREATE INDEX links_reverse ON links(workspace_id,to_id,to_version);
CREATE TABLE attachments (
 workspace_id uuid NOT NULL, id uuid NOT NULL, source_id uuid NOT NULL, source_kind text NOT NULL DEFAULT 'source' CHECK(source_kind='source'),
 name text NOT NULL, mime text NOT NULL, size bigint NOT NULL, sha256 text NOT NULL, content bytea NOT NULL,
 PRIMARY KEY(workspace_id,id), FOREIGN KEY(workspace_id,source_id,source_kind) REFERENCES objects(workspace_id,id,kind), CHECK(size=octet_length(content))
);
CREATE TABLE idempotency (
 workspace_id uuid REFERENCES workspaces(id), actor uuid REFERENCES api_keys(id), operation text, key text,
 digest text NOT NULL, response jsonb NOT NULL,
 PRIMARY KEY(workspace_id,actor,operation,key)
);
CREATE TABLE audit (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, workspace_id uuid NOT NULL, actor uuid NOT NULL REFERENCES api_keys(id),
 operation text NOT NULL, object_id uuid NOT NULL, version integer NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(workspace_id,object_id,version) REFERENCES revisions(workspace_id,object_id,version)
);
CREATE TABLE review_events (
 workspace_id uuid NOT NULL, id uuid NOT NULL, claim_id uuid NOT NULL, cause_id uuid NOT NULL, cause_version integer NOT NULL,
 event_type text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,id), FOREIGN KEY(workspace_id,claim_id) REFERENCES objects(workspace_id,id),
 FOREIGN KEY(workspace_id,cause_id,cause_version) REFERENCES revisions(workspace_id,object_id,version)
);
CREATE TABLE review_targets (
 workspace_id uuid NOT NULL, event_id uuid NOT NULL, knowledge_id uuid NOT NULL,
 resolved_by uuid, resolved_version integer, resolved_at timestamptz,
 PRIMARY KEY(workspace_id,event_id,knowledge_id),
 FOREIGN KEY(workspace_id,event_id) REFERENCES review_events(workspace_id,id),
 FOREIGN KEY(workspace_id,knowledge_id) REFERENCES objects(workspace_id,id),
 FOREIGN KEY(workspace_id,resolved_by,resolved_version) REFERENCES revisions(workspace_id,object_id,version)
);
CREATE INDEX review_pending ON review_targets(workspace_id,knowledge_id) WHERE resolved_at IS NULL;
CREATE TABLE event_resolutions (
 workspace_id uuid NOT NULL, event_id uuid NOT NULL, resolver_id uuid NOT NULL, resolver_version integer NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(workspace_id,event_id,resolver_id,resolver_version),
 FOREIGN KEY(workspace_id,event_id) REFERENCES review_events(workspace_id,id),
 FOREIGN KEY(workspace_id,resolver_id,resolver_version) REFERENCES revisions(workspace_id,object_id,version)
);

CREATE TABLE index_configs (fingerprint text PRIMARY KEY, model text NOT NULL, dimensions integer NOT NULL, active boolean NOT NULL DEFAULT false);
CREATE UNIQUE INDEX one_active_config ON index_configs(active) WHERE active;
CREATE TABLE index_jobs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, workspace_id uuid NOT NULL, object_id uuid NOT NULL, version integer NOT NULL,
 fingerprint text NOT NULL REFERENCES index_configs(fingerprint), state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','running','done','failed')),
 attempts integer NOT NULL DEFAULT 0, available_at timestamptz NOT NULL DEFAULT now(), lease_until timestamptz, lease_token uuid, last_error text,
 FOREIGN KEY(workspace_id,object_id,version) REFERENCES revisions(workspace_id,object_id,version),
 UNIQUE(workspace_id,object_id,version,fingerprint)
);
CREATE INDEX jobs_ready ON index_jobs(state,available_at,lease_until);
CREATE INDEX revisions_trgm ON revisions USING gin(body gin_trgm_ops);
CREATE INDEX revisions_fts ON revisions USING gin(to_tsvector('simple',body));
CREATE INDEX revisions_tags ON revisions USING gin(tags);

CREATE FUNCTION reject_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'immutable record' USING ERRCODE='23514'; END $$;
CREATE TRIGGER revisions_immutable BEFORE UPDATE OR DELETE ON revisions FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE TRIGGER links_immutable BEFORE UPDATE OR DELETE ON links FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE TRIGGER attachments_immutable BEFORE UPDATE OR DELETE ON attachments FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE TRIGGER audit_immutable BEFORE UPDATE OR DELETE ON audit FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE TRIGGER resolutions_immutable BEFORE UPDATE OR DELETE ON event_resolutions FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE TRIGGER events_immutable BEFORE UPDATE OR DELETE ON review_events FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE FUNCTION guard_object() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR NEW.workspace_id<>OLD.workspace_id OR NEW.id<>OLD.id OR NEW.kind<>OLD.kind OR NEW.created_at<>OLD.created_at OR NEW.current_version<>OLD.current_version+1 OR OLD.kind IN ('verification','assessment') THEN
 RAISE EXCEPTION 'invalid object mutation' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER objects_guard BEFORE UPDATE OR DELETE ON objects FOR EACH ROW EXECUTE FUNCTION guard_object();
CREATE FUNCTION guard_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE prev revisions; target_kind text;
BEGIN
 IF NEW.version>1 THEN
 SELECT * INTO STRICT prev FROM revisions WHERE workspace_id=NEW.workspace_id AND object_id=NEW.object_id AND version=NEW.version-1;
 IF NEW.kind='source' AND (to_jsonb(NEW)-ARRAY['version','tags','author','reason','created_at','created_tx']) IS DISTINCT FROM (to_jsonb(prev)-ARRAY['version','tags','author','reason','created_at','created_tx']) THEN
 RAISE EXCEPTION 'source content is immutable' USING ERRCODE='23514'; END IF;
 END IF;
 IF NEW.claim_id IS NOT NULL THEN
 SELECT kind INTO target_kind FROM objects WHERE workspace_id=NEW.workspace_id AND id=NEW.claim_id;
 IF target_kind IS DISTINCT FROM 'claim' THEN RAISE EXCEPTION 'claim reference required' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER revision_guard BEFORE INSERT ON revisions FOR EACH ROW EXECUTE FUNCTION guard_revision();
CREATE FUNCTION guard_link() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE f revisions; t revisions;
BEGIN
 SELECT * INTO STRICT f FROM revisions WHERE workspace_id=NEW.workspace_id AND object_id=NEW.from_id AND version=NEW.from_version;
 SELECT * INTO STRICT t FROM revisions WHERE workspace_id=NEW.workspace_id AND object_id=NEW.to_id AND version=NEW.to_version;
 IF f.created_tx<>txid_current() THEN RAISE EXCEPTION 'cannot append links to committed revision' USING ERRCODE='23514'; END IF;
 IF NOT ((f.kind='claim' AND NEW.role='source' AND t.kind='source') OR
 (f.kind='verification' AND NEW.role='evidence' AND t.kind='source') OR
 (f.kind='assessment' AND NEW.role='verification' AND t.kind='verification' AND f.claim_id=t.claim_id AND f.claim_version=t.claim_version) OR
 (f.kind='knowledge' AND ((NEW.role='claim' AND t.kind='claim') OR (NEW.role='assessment' AND t.kind='assessment')))) THEN
 RAISE EXCEPTION 'invalid relation' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER links_guard BEFORE INSERT ON links FOR EACH ROW EXECUTE FUNCTION guard_link();
CREATE FUNCTION validate_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM objects WHERE workspace_id=NEW.workspace_id AND id=NEW.object_id AND current_version=NEW.version) THEN RAISE EXCEPTION 'revision must be current on creation' USING ERRCODE='23514'; END IF;
 IF NEW.kind IN ('claim','verification','assessment') AND NOT EXISTS(SELECT 1 FROM links WHERE workspace_id=NEW.workspace_id AND from_id=NEW.object_id AND from_version=NEW.version) THEN
 RAISE EXCEPTION 'evidence required' USING ERRCODE='23514'; END IF;
 IF NEW.kind='knowledge' THEN
 IF NOT EXISTS(SELECT 1 FROM links l JOIN revisions r ON (r.workspace_id,r.object_id,r.version)=(l.workspace_id,l.to_id,l.to_version)
 WHERE l.workspace_id=NEW.workspace_id AND l.from_id=NEW.object_id AND l.from_version=NEW.version AND l.role='assessment' AND r.conclusion='support') THEN
 RAISE EXCEPTION 'support assessment required' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM links l JOIN revisions r ON (r.workspace_id,r.object_id,r.version)=(l.workspace_id,l.to_id,l.to_version)
 WHERE l.workspace_id=NEW.workspace_id AND l.from_id=NEW.object_id AND l.from_version=NEW.version AND l.role='assessment'
 AND NOT EXISTS(SELECT 1 FROM links c WHERE (c.workspace_id,c.from_id,c.from_version)=(l.workspace_id,l.from_id,l.from_version) AND c.role='claim' AND c.to_id=r.claim_id AND c.to_version=r.claim_version)) THEN
 RAISE EXCEPTION 'assessment claim revision must be linked' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER revision_complete AFTER INSERT ON revisions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION validate_revision();

CREATE FUNCTION guard_attachment_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM revisions WHERE workspace_id=NEW.workspace_id AND object_id=NEW.source_id AND version=1 AND created_tx=txid_current()) THEN
 RAISE EXCEPTION 'attachments must be created with source' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER attachments_insert_guard BEFORE INSERT ON attachments FOR EACH ROW EXECUTE FUNCTION guard_attachment_insert();
