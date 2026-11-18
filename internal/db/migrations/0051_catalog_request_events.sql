-- Request webhooks (roadmap M15-10): each change of a catalog request's
-- status, recorded with the change itself so that none is missed, until the
-- leader sends it to the item's request webhooks.
CREATE TABLE catalog_request_events (
    id bigserial PRIMARY KEY,
    request_id bigint NOT NULL,
    status text NOT NULL,
    previous text NOT NULL DEFAULT '',
    created timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION catalog_request_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO catalog_request_events (request_id, status) VALUES (NEW.id, NEW.status);
    ELSIF NEW.status IS DISTINCT FROM OLD.status THEN
        INSERT INTO catalog_request_events (request_id, status, previous) VALUES (NEW.id, NEW.status, OLD.status);
    END IF;
    RETURN NULL;
END $$;

CREATE TRIGGER catalog_request_changed AFTER INSERT OR UPDATE OF status ON catalog_requests
    FOR EACH ROW EXECUTE FUNCTION catalog_request_changed();
