CREATE TABLE workplace_links (
 workplace_id bigint NOT NULL REFERENCES workplaces(id),
 linked_id bigint NOT NULL REFERENCES workplaces(id),
 PRIMARY KEY(workplace_id,linked_id), CHECK(workplace_id<>linked_id)
);
CREATE INDEX ON workplace_links(linked_id);
CREATE VIEW workplace_resources AS
 SELECT id AS workplace_id,id AS resource_id FROM workplaces
 UNION SELECT workplace_id,linked_id FROM workplace_links;

-- A booking retains the physical places it occupied when it was created.
CREATE TABLE booking_resources (
 booking_id bigint NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
 resource_id bigint NOT NULL REFERENCES workplaces(id),
 starts_at timestamptz NOT NULL, ends_at timestamptz NOT NULL,
 cancelled_at timestamptz,
 PRIMARY KEY(booking_id,resource_id),
 EXCLUDE USING gist (resource_id WITH =, tstzrange(starts_at,ends_at,'[)') WITH &&) WHERE (cancelled_at IS NULL)
);
INSERT INTO booking_resources SELECT id,workplace_id,starts_at,ends_at,cancelled_at FROM bookings;
CREATE FUNCTION sync_booking_resources() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  INSERT INTO booking_resources(booking_id,resource_id,starts_at,ends_at,cancelled_at)
   SELECT NEW.id,resource_id,NEW.starts_at,NEW.ends_at,NEW.cancelled_at
   FROM workplace_resources WHERE workplace_id=NEW.workplace_id;
 ELSE
  UPDATE booking_resources SET starts_at=NEW.starts_at,ends_at=NEW.ends_at,cancelled_at=NEW.cancelled_at
   WHERE booking_id=NEW.id;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER booking_resources_sync AFTER INSERT OR UPDATE OF starts_at,ends_at,cancelled_at ON bookings
 FOR EACH ROW EXECUTE FUNCTION sync_booking_resources();
