CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
CREATE TABLE workspaces(id uuid PRIMARY KEY,name text NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE api_keys(id uuid PRIMARY KEY,name text NOT NULL,digest bytea UNIQUE NOT NULL,revoked_at timestamptz,created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE key_permissions(key_id uuid REFERENCES api_keys(id),workspace_id uuid REFERENCES workspaces(id),can_write boolean NOT NULL,PRIMARY KEY(key_id,workspace_id));
CREATE TABLE sources(
 workspace_id uuid NOT NULL REFERENCES workspaces(id),id uuid NOT NULL,body text NOT NULL,
 provenance text NOT NULL CHECK(provenance<>''),source_time timestamptz,metadata jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(metadata)='object'),
 tags text[] NOT NULL DEFAULT '{}',author uuid NOT NULL REFERENCES api_keys(id),created_at timestamptz NOT NULL DEFAULT now(),created_tx bigint NOT NULL DEFAULT txid_current(),
 PRIMARY KEY(workspace_id,id)
);
CREATE TABLE attachments(
 workspace_id uuid NOT NULL,id uuid NOT NULL,source_id uuid NOT NULL,name text NOT NULL,mime text NOT NULL,size bigint NOT NULL,sha256 text NOT NULL,content bytea NOT NULL,
 PRIMARY KEY(workspace_id,id),FOREIGN KEY(workspace_id,source_id) REFERENCES sources(workspace_id,id),CHECK(size=octet_length(content))
);
CREATE TABLE knowledge(
 workspace_id uuid NOT NULL REFERENCES workspaces(id),id uuid NOT NULL,current_revision integer NOT NULL CHECK(current_revision>0),created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,id)
);
CREATE TABLE knowledge_revisions(
 workspace_id uuid NOT NULL,knowledge_id uuid NOT NULL,revision integer NOT NULL CHECK(revision>0),body text NOT NULL CHECK(char_length(body) BETWEEN 1 AND 2000),
 scope jsonb NOT NULL CHECK(jsonb_typeof(scope)='object'),conditions text NOT NULL DEFAULT '',tags text[] NOT NULL DEFAULT '{}',reason text NOT NULL CHECK(reason<>''),
 author uuid NOT NULL REFERENCES api_keys(id),created_at timestamptz NOT NULL DEFAULT now(),created_tx bigint NOT NULL DEFAULT txid_current(),
 PRIMARY KEY(workspace_id,knowledge_id,revision),FOREIGN KEY(workspace_id,knowledge_id) REFERENCES knowledge(workspace_id,id)
);
ALTER TABLE knowledge ADD FOREIGN KEY(workspace_id,id,current_revision) REFERENCES knowledge_revisions(workspace_id,knowledge_id,revision) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE knowledge_sources(
 workspace_id uuid NOT NULL,knowledge_id uuid NOT NULL,revision integer NOT NULL,source_id uuid NOT NULL,
 PRIMARY KEY(workspace_id,knowledge_id,revision,source_id),
 FOREIGN KEY(workspace_id,knowledge_id,revision) REFERENCES knowledge_revisions(workspace_id,knowledge_id,revision),
 FOREIGN KEY(workspace_id,source_id) REFERENCES sources(workspace_id,id)
);
CREATE TABLE idempotency(workspace_id uuid REFERENCES workspaces(id),actor uuid REFERENCES api_keys(id),operation text,key text,digest text NOT NULL,response jsonb NOT NULL,PRIMARY KEY(workspace_id,actor,operation,key));
CREATE INDEX knowledge_scope ON knowledge_revisions USING gin(scope jsonb_path_ops);
CREATE INDEX knowledge_tags ON knowledge_revisions USING gin(tags);
CREATE INDEX knowledge_text ON knowledge_revisions USING gin(body gin_trgm_ops);
CREATE INDEX source_text ON sources USING gin(body gin_trgm_ops);
CREATE FUNCTION reject_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'immutable record' USING ERRCODE='23514'; END $$;
CREATE TRIGGER sources_immutable BEFORE UPDATE OR DELETE ON sources FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE TRIGGER attachments_immutable BEFORE UPDATE OR DELETE ON attachments FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE TRIGGER revisions_immutable BEFORE UPDATE OR DELETE ON knowledge_revisions FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE TRIGGER relations_immutable BEFORE UPDATE OR DELETE ON knowledge_sources FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE FUNCTION guard_knowledge() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' OR NEW.workspace_id<>OLD.workspace_id OR NEW.id<>OLD.id OR NEW.created_at<>OLD.created_at OR NEW.current_revision<>OLD.current_revision+1 THEN RAISE EXCEPTION 'invalid revision advance' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$;
CREATE TRIGGER knowledge_guard BEFORE UPDATE OR DELETE ON knowledge FOR EACH ROW EXECUTE FUNCTION guard_knowledge();
CREATE FUNCTION guard_relation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM knowledge_revisions WHERE workspace_id=NEW.workspace_id AND knowledge_id=NEW.knowledge_id AND revision=NEW.revision AND created_tx=txid_current()) THEN RAISE EXCEPTION 'cannot append evidence to committed revision' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$;
CREATE TRIGGER relation_guard BEFORE INSERT ON knowledge_sources FOR EACH ROW EXECUTE FUNCTION guard_relation();
CREATE FUNCTION guard_attachment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM sources WHERE workspace_id=NEW.workspace_id AND id=NEW.source_id AND created_tx=txid_current()) THEN RAISE EXCEPTION 'cannot append attachment to committed source' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$;
CREATE TRIGGER attachment_guard BEFORE INSERT ON attachments FOR EACH ROW EXECUTE FUNCTION guard_attachment();
CREATE FUNCTION validate_revision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM knowledge WHERE workspace_id=NEW.workspace_id AND id=NEW.knowledge_id AND current_revision=NEW.revision) OR NOT EXISTS(SELECT 1 FROM knowledge_sources WHERE workspace_id=NEW.workspace_id AND knowledge_id=NEW.knowledge_id AND revision=NEW.revision) THEN RAISE EXCEPTION 'current revision and evidence required' USING ERRCODE='23514'; END IF;
 RETURN NULL; END $$;
CREATE CONSTRAINT TRIGGER revision_complete AFTER INSERT ON knowledge_revisions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION validate_revision();
