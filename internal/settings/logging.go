package settings

import (
	"errors"
	"fmt"
	"strconv"
)

// AWX's external logging settings (awx/main/conf.py), with its defaults.
func init() {
	atLeastOne := func(v any) error {
		if n, err := strconv.ParseFloat(fmt.Sprint(v), 64); err == nil && n < 1 {
			return errors.New("Ensure this value is greater than or equal to 1.")
		}
		return nil
	}
	for _, d := range []*Def{
		{Key: "LOG_AGGREGATOR_HOST", Type: "string", Default: nil, Nullable: true, Label: "Logging Aggregator",
			Help: "Hostname/IP where external logs will be sent to. With HTTPS/HTTP, a URL: https:// is assumed unless http:// is given."},
		{Key: "LOG_AGGREGATOR_PORT", Type: "integer", Default: nil, Nullable: true, Label: "Logging Aggregator Port",
			Help: "Port on Logging Aggregator to send logs to (if required and not provided in Logging Aggregator)."},
		{Key: "LOG_AGGREGATOR_TYPE", Type: "choice", Default: nil, Nullable: true, Label: "Logging Aggregator Type",
			Choices: [][]any{{"logstash", "logstash"}, {"splunk", "splunk"}, {"loggly", "loggly"}, {"sumologic", "sumologic"}, {"other", "other"}},
			Help:    "Format messages for the chosen log aggregator."},
		{Key: "LOG_AGGREGATOR_USERNAME", Type: "string", Default: "", Label: "Logging Aggregator Username",
			Help: "Username for external log aggregator (if required; HTTP/s only)."},
		{Key: "LOG_AGGREGATOR_PASSWORD", Type: "string", Default: "", Secret: true, Label: "Logging Aggregator Password/Token",
			Help: "Password or authentication token for external log aggregator (if required; HTTP/s only). Splunk's HTTP Event Collector token goes here."},
		{Key: "LOG_AGGREGATOR_LOGGERS", Type: "list", Default: []any{"awx", "activity_stream", "job_events", "system_tracking", "broadcast_websocket"},
			Label: "Loggers Sending Data to Log Aggregator Form",
			Help:  "Loggers that send logs to the aggregator: awx (service logs), activity_stream (activity stream records), job_events (job events), system_tracking (facts gathered by jobs), broadcast_websocket (none in awxnext)."},
		{Key: "LOG_AGGREGATOR_INDIVIDUAL_FACTS", Type: "boolean", Default: false, Label: "Log System Tracking Facts Individually",
			Help: "If set, system tracking facts are sent one message per fact, allowing for greater search query granularity. If unset, the facts of a host are sent as a single dictionary."},
		{Key: "LOG_AGGREGATOR_ENABLED", Type: "boolean", Default: false, Label: "Enable External Logging",
			Help: "Enable sending logs to external log aggregator."},
		{Key: "LOG_AGGREGATOR_TOWER_UUID", Type: "string", Default: "", Label: "Cluster-wide unique identifier.",
			Help: "Useful to uniquely identify instances: sent as tower_uuid (INSTALL_UUID when empty)."},
		{Key: "LOG_AGGREGATOR_PROTOCOL", Type: "choice", Default: "https", Label: "Logging Aggregator Protocol",
			Choices: [][]any{{"https", "HTTPS/HTTP"}, {"tcp", "TCP"}, {"udp", "UDP"}},
			Help:    "Protocol used to communicate with log aggregator. HTTPS/HTTP assumes HTTPS unless http:// is explicitly used in the Logging Aggregator hostname."},
		{Key: "LOG_AGGREGATOR_TCP_TIMEOUT", Type: "integer", Default: 5, Label: "TCP Connection Timeout",
			Help: "Number of seconds for a TCP connection to external log aggregator to timeout, and between retries. Applies to HTTPS and TCP log aggregator protocols."},
		{Key: "LOG_AGGREGATOR_VERIFY_CERT", Type: "boolean", Default: true, Label: "Enable/disable HTTPS certificate verification",
			Help: `Flag to control enable/disable of certificate verification when LOG_AGGREGATOR_PROTOCOL is "https".`},
		{Key: "LOG_AGGREGATOR_LEVEL", Type: "choice", Default: "WARNING", Label: "Logging Aggregator Level Threshold",
			Choices: [][]any{{"DEBUG", "DEBUG"}, {"INFO", "INFO"}, {"WARNING", "WARNING"}, {"ERROR", "ERROR"}, {"CRITICAL", "CRITICAL"}},
			Help:    "Level threshold used by log handler. Messages less severe than the threshold are not sent: activity stream, job event and fact messages are INFO, so they need INFO or DEBUG."},
		{Key: "LOG_AGGREGATOR_ACTION_QUEUE_SIZE", Type: "integer", Default: 131072, Validate: atLeastOne, Label: "Maximum number of messages that can be stored in the log action queue",
			Help: "How many messages wait for the aggregator. At 90% of it, INFO and DEBUG messages are discarded; when it is full, every new message is."},
		{Key: "LOG_AGGREGATOR_ACTION_MAX_DISK_USAGE_GB", Type: "integer", Default: 1, Validate: atLeastOne, CompatOnly: true, Label: "Maximum disk persistence for rsyslogd action queuing (in GB)",
			Help: "No effect in awxnext: there is no rsyslogd, and the queue is kept in memory."},
		{Key: "LOG_AGGREGATOR_MAX_DISK_USAGE_PATH", Type: "string", Default: "/var/lib/awx", CompatOnly: true, Label: "File system location for rsyslogd disk persistence",
			Help: "No effect in awxnext: there is no rsyslogd, and the queue is kept in memory."},
		{Key: "LOG_AGGREGATOR_RSYSLOGD_DEBUG", Type: "boolean", Default: false, Label: "Enable rsyslogd debugging",
			Help: "Log each delivery to the aggregator, and its errors, on the server's own log. Useful for debugging connection issues for external log aggregation."},
	} {
		d.Category = "logging"
		Register(d)
	}
}
