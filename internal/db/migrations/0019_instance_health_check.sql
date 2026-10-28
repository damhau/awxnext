-- When a health check was last asked for (AWX's health_check_started): it
-- is pending until last_health_check is later.
ALTER TABLE instances ADD COLUMN health_check_started timestamptz;
