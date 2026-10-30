-- Instance group membership now follows the groups' policies. Memberships
-- made before were manual: list their instances in the group's
-- policy_instance_list, as AWX's attach does, so the policies keep them.
-- The installer's groups take every instance by percentage instead.
UPDATE instance_groups g SET policy_instance_list = g.policy_instance_list || coalesce((
        SELECT jsonb_agg(i.hostname ORDER BY i.hostname) FROM instance_group_instances gi JOIN instances i ON i.id = gi.instance_id
        WHERE gi.instance_group_id = g.id AND NOT g.policy_instance_list ? i.hostname), '[]'::jsonb)
    WHERE NOT g.is_container_group AND g.name NOT IN ('default', 'controlplane');
