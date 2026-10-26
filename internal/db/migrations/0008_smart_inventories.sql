-- Smart inventories: host_filter compiled to SQL (values inlined as escaped literals).
ALTER TABLE inventories ADD COLUMN host_filter_sql text NOT NULL DEFAULT '';

-- Hosts of a smart inventory: hosts of the organization's regular inventories
-- matching the filter, one per host name (like AWX).
CREATE FUNCTION smart_inventory_hosts(inv bigint) RETURNS TABLE(host_id bigint) LANGUAGE plpgsql STABLE AS $$
DECLARE
    f text;
    org bigint;
BEGIN
    SELECT host_filter_sql, organization_id INTO f, org FROM inventories WHERE id = inv AND kind = 'smart';
    IF f IS NULL OR f = '' THEN
        RETURN;
    END IF;
    RETURN QUERY EXECUTE
        'SELECT DISTINCT ON (t.name) t.id FROM hosts t JOIN inventories i ON i.id = t.inventory_id '
        || 'WHERE i.organization_id = $1 AND i.kind = '''' AND (' || f || ') ORDER BY t.name, t.id'
        USING org;
END
$$;
