-- AWX's HostMetricSummaryMonthly: per month, the hosts first automated
-- (hosts_added), those soft-deleted (hosts_deleted) and the running total
-- (license_consumed), computed by the host_metric_summary_monthly
-- management job.
CREATE TABLE host_metric_summary_monthly (
    id bigserial PRIMARY KEY,
    date date NOT NULL UNIQUE,
    license_consumed bigint NOT NULL DEFAULT 0,
    license_capacity bigint NOT NULL DEFAULT 0,
    hosts_added integer NOT NULL DEFAULT 0,
    hosts_deleted integer NOT NULL DEFAULT 0,
    indirectly_managed_hosts integer NOT NULL DEFAULT 0
);
