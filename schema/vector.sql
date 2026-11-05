CREATE TABLE chunks (
 workspace_id uuid NOT NULL, object_id uuid NOT NULL, version integer NOT NULL, fingerprint text NOT NULL REFERENCES index_configs(fingerprint),
 position integer NOT NULL, start_rune integer NOT NULL, content text NOT NULL, embedding public.vector NOT NULL,
 PRIMARY KEY(workspace_id,object_id,version,fingerprint,position),
 FOREIGN KEY(workspace_id,object_id,version) REFERENCES revisions(workspace_id,object_id,version)
);
CREATE FUNCTION guard_chunk() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM index_configs WHERE fingerprint=NEW.fingerprint AND dimensions=public.vector_dims(NEW.embedding)) THEN
 RAISE EXCEPTION 'embedding dimension mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER chunks_guard BEFORE INSERT OR UPDATE ON chunks FOR EACH ROW EXECUTE FUNCTION guard_chunk();
